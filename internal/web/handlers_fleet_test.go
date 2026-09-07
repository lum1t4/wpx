//go:build linux

package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"github.com/lum1t4/wpx/internal/store"
)

type fleetTestBroker struct {
	mu  sync.Mutex
	run func(model.Site) (broker.WordPressInventoryResult, error)
}

func (b *fleetTestBroker) Call(_ context.Context, operation broker.Operation, _ string, input any, out any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if operation != broker.OpWordPressInventory {
		return nil
	}
	if b.run != nil {
		result, err := b.run(input.(broker.WordPressPluginsRequest).Site)
		if err != nil {
			return err
		}
		*out.(*broker.WordPressInventoryResult) = result
		return nil
	}
	*out.(*broker.WordPressInventoryResult) = broker.WordPressInventoryResult{CoreVersion: "6.8.3", Plugins: []broker.WordPressPlugin{{Name: "akismet", Version: "5.0", Update: "available", UpdateVersion: "5.1"}}}
	return nil
}

func fleetWebFixture(t *testing.T) (*Server, storeUserFixture) {
	t.Helper()
	server, owner, _ := navigationServer(t)
	server.broker = &fleetTestBroker{}
	for _, site := range []model.Site{
		{ID: "fleet-web-one", Domain: "one.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "fleet-web-two", Domain: "two.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
	} {
		navigationSite(t, server, owner, site)
	}
	if err := server.store.ConfigureSecretKey(bytes.Repeat([]byte{5}, 32)); err != nil {
		t.Fatal(err)
	}
	target, _, err := server.store.CreateS3Target(context.Background(), owner, model.BackupTarget{Name: "Fleet recovery", Endpoint: "https://objects.example.com", Bucket: "wpx", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(context.Background())
	if err != nil || !found {
		t.Fatalf("claim target verification: found=%t error=%v", found, err)
	}
	if err := server.store.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	return server, storeUserFixture{owner: owner, targetID: target.ID}
}

type storeUserFixture struct {
	owner    store.User
	targetID string
}

func TestFleetBatchRejectsUnauthorizedCrossSiteSelectionBeforeQueueing(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	collaborator, err := server.store.CreateUser(context.Background(), fixture.owner, "fleet-collaborator", "a-secure-test-password", rbac.Collaborator, []string{"fleet-web-one"})
	if err != nil {
		t.Fatal(err)
	}
	response := navigationRequest(t, server, collaborator, http.MethodPost, "/wordpress/plugins/update", url.Values{
		"target_id": {fixture.targetID}, "update": {"fleet-web-one|akismet", "fleet-web-two|akismet"},
	})
	requireNavigationStatus(t, response, http.StatusForbidden)
	jobs, err := server.store.RecentJobsForUser(context.Background(), collaborator, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.Kind == "wordpress.update" {
			t.Fatalf("unauthorized request queued update job %#v", job)
		}
	}
}

func TestFleetBatchRejectsEmptyMalformedAndDuplicateSelections(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	for name, updates := range map[string][]string{
		"empty": nil, "malformed": {"fleet-web-one/akismet"}, "duplicate": {"fleet-web-one|akismet", "fleet-web-one|akismet"}, "not offered": {"fleet-web-one|invented-plugin"},
	} {
		t.Run(name, func(t *testing.T) {
			response := navigationRequest(t, server, fixture.owner, http.MethodPost, "/wordpress/plugins/update", url.Values{"target_id": {fixture.targetID}, "update": updates})
			requireNavigationStatus(t, response, http.StatusBadRequest)
		})
	}
}

func TestFleetBatchQueuesDurableUpdatesAndReportsQueuedOutcomes(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	response := navigationRequest(t, server, fixture.owner, http.MethodPost, "/wordpress/plugins/update", url.Values{
		"target_id": {fixture.targetID}, "update": {"fleet-web-one|akismet", "fleet-web-two|akismet"},
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	body := response.Body.String()
	if strings.Count(body, ">Queued</span>") != 2 || strings.Contains(body, ">Completed</span>") {
		t.Fatalf("batch response did not report two background jobs: %s", body)
	}
	jobs, err := server.store.RecentJobsForUser(context.Background(), fixture.owner, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, job := range jobs {
		if job.Kind == "wordpress.update" && job.Status == "queued" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("queued update count = %d, want 2", count)
	}
}

func TestFleetInventoryFailureIsIsolatedToItsSite(t *testing.T) {
	server, fixture := fleetWebFixture(t)
	server.broker = &fleetTestBroker{run: func(site model.Site) (broker.WordPressInventoryResult, error) {
		if site.ID == "fleet-web-one" {
			return broker.WordPressInventoryResult{}, errors.New("site WP-CLI failed")
		}
		return broker.WordPressInventoryResult{CoreVersion: "6.8.3", Plugins: []broker.WordPressPlugin{{Name: "healthy-plugin", Version: "2.0"}}}, nil
	}}
	response := navigationRequest(t, server, fixture.owner, http.MethodGet, "/wordpress", nil)
	requireNavigationStatus(t, response, http.StatusOK)
	body := response.Body.String()
	if !strings.Contains(body, "one.example.com") || !strings.Contains(body, "WordPress inventory could not be loaded") || !strings.Contains(body, "two.example.com") || !strings.Contains(body, "healthy-plugin") {
		t.Fatalf("one failed inventory hid another site's result: %s", body)
	}
}
