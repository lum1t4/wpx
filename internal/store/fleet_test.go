package store

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func fleetStoreFixture(t *testing.T) (*Store, context.Context, User, model.BackupTarget) {
	t.Helper()
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "fleet-owner", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []model.Site{
		{ID: "fleet-one", Domain: "one.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
		{ID: "fleet-two", Domain: "two.example.com", Kind: model.WordPress, PHPVersion: "8.4"},
	} {
		if _, err := state.CreateSite(ctx, owner, site); err != nil {
			t.Fatal(err)
		}
		job, found, err := state.ClaimNextJob(ctx)
		if err != nil || !found {
			t.Fatalf("claim site job: found=%t error=%v", found, err)
		}
		if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
			t.Fatal(err)
		}
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Fleet recovery", Endpoint: "https://objects.example.com", Bucket: "wpx", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim storage job: found=%t error=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	return state, ctx, owner, target
}

func TestFleetPluginUpdatesQueueOrdinaryDurableJobs(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	jobs, err := state.EnqueueFleetPluginUpdates(ctx, owner, target.ID, []FleetPluginUpdate{
		{SiteID: "fleet-one", Plugin: "akismet"},
		{SiteID: "fleet-one", Plugin: "query-monitor"},
		{SiteID: "fleet-two", Plugin: "woocommerce"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("queued jobs = %d, want 3", len(jobs))
	}
	for _, queued := range jobs {
		job, err := state.Job(ctx, queued.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Kind != "wordpress.update" || job.Status != "queued" || job.TargetID != queued.SiteID || !strings.Contains(job.PayloadJSON, `"component":"plugin"`) || !strings.Contains(job.PayloadJSON, `"name":"`+queued.Plugin+`"`) || !strings.Contains(job.PayloadJSON, `"target_id":"`+target.ID+`"`) {
			t.Fatalf("unexpected queued fleet job: %#v", job)
		}
	}
}

func TestFleetPluginUpdatesAuthorizeWholeBatch(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	collaborator, err := state.CreateUser(ctx, owner, "fleet-helper", "a-secure-test-password", rbac.Collaborator, []string{"fleet-one"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.EnqueueFleetPluginUpdates(ctx, collaborator, target.ID, []FleetPluginUpdate{{SiteID: "fleet-one", Plugin: "akismet"}, {SiteID: "fleet-two", Plugin: "woocommerce"}})
	if err == nil || err.Error() != "permission denied" {
		t.Fatalf("cross-site batch error = %v, want permission denied", err)
	}
	var count int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='wordpress.update'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unauthorized batch queued %d jobs: %v", count, err)
	}
}

func TestFleetPluginUpdatesRejectInvalidSelectionsAndExistingWork(t *testing.T) {
	state, ctx, owner, target := fleetStoreFixture(t)
	for name, updates := range map[string][]FleetPluginUpdate{
		"empty":     nil,
		"malformed": {{SiteID: "fleet-one", Plugin: "../escape"}},
		"duplicate": {{SiteID: "fleet-one", Plugin: "akismet"}, {SiteID: "fleet-one", Plugin: "akismet"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := state.EnqueueFleetPluginUpdates(ctx, owner, target.ID, updates); err == nil {
				t.Fatal("invalid selection was accepted")
			}
		})
	}
	if _, err := state.EnqueueWordPressUpdate(ctx, owner, "fleet-one", target.ID, model.WordPressUpdate{Component: model.WordPressPlugin, Name: "existing-work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueFleetPluginUpdates(ctx, owner, target.ID, []FleetPluginUpdate{{SiteID: "fleet-one", Plugin: "akismet"}}); err == nil || !strings.Contains(err.Error(), "wait for all operations") {
		t.Fatalf("conflicting fleet update error = %v", err)
	}
}
