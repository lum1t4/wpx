//go:build linux

package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

var generatedSiteID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestSiteCreationGeneratesIDsForEveryKind(t *testing.T) {
	server, owner, _ := navigationServer(t)
	seen := make(map[string]bool)
	for index, kind := range []model.SiteKind{model.WordPress, model.PHP, model.Python, model.Static, model.ReverseProxy} {
		t.Run(string(kind), func(t *testing.T) {
			page := navigationRequest(t, server, owner, http.MethodGet, "/sites/new?kind="+string(kind), nil)
			requireNavigationStatus(t, page, http.StatusOK)
			if strings.Contains(page.Body.String(), `name="id"`) {
				t.Fatal("creation form asks the user to choose an internal identifier")
			}
			form := url.Values{"domain": {fmt.Sprintf("site-%d.example.com", index)}, "kind": {string(kind)}}
			if kind == model.WordPress || kind == model.PHP {
				form.Set("php_version", "8.4")
			}
			if kind == model.ReverseProxy {
				form.Set("upstream", "http://127.0.0.1:3000")
			}
			// Old forms and forged requests must not regain control of filenames
			// by submitting an identifier, even if it is otherwise a valid UUID.
			if index == 1 {
				form.Set("id", "../../outside")
			} else if index == 2 {
				form.Set("id", "1c43f941-7890-4eaf-a117-c53568928a12")
			} else if index == 3 {
				form.Set("id", "old-form-slug")
			}
			response := navigationRequest(t, server, owner, http.MethodPost, "/sites", form)
			requireNavigationStatus(t, response, http.StatusSeeOther)
			location, err := url.Parse(response.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			id := strings.TrimPrefix(location.Path, "/sites/")
			if !generatedSiteID.MatchString(id) || id == form.Get("id") || seen[id] {
				t.Fatalf("creation did not allocate an independent UUID: %q", id)
			}
			seen[id] = true
			site, err := server.store.Site(context.Background(), id)
			if err != nil || site.Domain != form.Get("domain") || site.Kind != kind {
				t.Fatalf("saved site = %#v, error = %v", site, err)
			}
			assertCreationJob(t, server, "site.provision", id)
			requireNavigationStatus(t, navigationRequest(t, server, owner, http.MethodGet, location.String(), nil), http.StatusOK)
		})
	}
}

func TestStagingGeneratesIDAndKeepsLegacyParent(t *testing.T) {
	server, owner, _ := navigationServer(t)
	parent := model.Site{ID: "existing-wordpress", Domain: "production.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	navigationSite(t, server, owner, parent)
	page := navigationRequest(t, server, owner, http.MethodGet, "/sites/"+parent.ID+"/staging", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	if strings.Contains(page.Body.String(), `name="id"`) {
		t.Fatal("staging form still asks for an internal identifier")
	}
	seen := make(map[string]bool)
	for index, submittedID := range []string{"", "../../forged-staging"} {
		form := url.Values{"domain": {fmt.Sprintf("staging-%d.example.com", index)}}
		if submittedID != "" {
			form.Set("id", submittedID)
		}
		response := navigationRequest(t, server, owner, http.MethodPost, "/sites/"+parent.ID+"/staging", form)
		requireNavigationStatus(t, response, http.StatusOK)
		staging := siteByDomain(t, server, form.Get("domain"))
		if !generatedSiteID.MatchString(staging.ID) || seen[staging.ID] || staging.ParentSiteID != parent.ID || staging.Environment != "staging" {
			t.Fatalf("staging identity or legacy parent changed: %#v", staging)
		}
		seen[staging.ID] = true
		if !navigationLinks(response.Body.String())["/sites/"+staging.ID] {
			t.Fatal("staging confirmation does not link to its generated identity")
		}
		assertCreationJob(t, server, "wordpress.staging_create", staging.ID)
	}
	persistedParent, err := server.store.Site(context.Background(), parent.ID)
	if err != nil || persistedParent.ID != parent.ID || persistedParent.Domain != parent.Domain {
		t.Fatalf("creating staging altered the legacy parent: %#v, error = %v", persistedParent, err)
	}
}

func TestBackupClonesGenerateIDs(t *testing.T) {
	server, owner, _ := navigationServer(t)
	// A digit-leading UUID must work everywhere a legacy slug previously did:
	// persistence, permission checks, routes, snapshot ownership, and cloning.
	source := model.Site{ID: "1c43f941-7890-4eaf-a117-c53568928a12", Domain: "source.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	navigationSite(t, server, owner, source)
	snapshotID := siteIDSnapshot(t, server, owner, source.ID)
	page := navigationRequest(t, server, owner, http.MethodGet, "/sites/"+source.ID+"/backups", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	cloneAction := "/sites/" + source.ID + "/backups/" + snapshotID + "/clone"
	formPattern := regexp.MustCompile(`(?s)<form\b[^>]*action="` + regexp.QuoteMeta(cloneAction) + `"[^>]*>.*?</form>`)
	cloneForm := formPattern.FindString(page.Body.String())
	if cloneForm == "" || strings.Contains(cloneForm, `name="target_id"`) || strings.Contains(cloneForm, `name="id"`) {
		t.Fatal("backup clone form is missing or still asks for a site identifier")
	}
	seen := map[string]bool{source.ID: true}
	for _, destination := range []string{"production", "staging"} {
		t.Run(destination, func(t *testing.T) {
			form := url.Values{"destination": {destination}, "target_domain": {destination + ".example.com"}}
			if destination == "staging" {
				form.Set("target_id", source.ID)
				form.Set("id", "../../outside")
			}
			response := navigationRequest(t, server, owner, http.MethodPost, cloneAction, form)
			if destination == "production" {
				requireNavigationStatus(t, response, http.StatusSeeOther)
			} else {
				requireNavigationStatus(t, response, http.StatusOK)
			}
			clone := siteByDomain(t, server, form.Get("target_domain"))
			if !generatedSiteID.MatchString(clone.ID) || seen[clone.ID] || clone.Environment != destination {
				t.Fatalf("invalid clone identity or environment: %#v", clone)
			}
			seen[clone.ID] = true
			if destination == "production" {
				if response.Header().Get("Location") != "/sites/"+clone.ID+"?clone=queued" || clone.ParentSiteID != "" {
					t.Fatal("independent clone did not retain its generated identity")
				}
			} else if clone.ParentSiteID != source.ID || !navigationLinks(response.Body.String())["/sites/"+clone.ID] {
				t.Fatal("protected clone did not retain its generated identity and parent")
			}
			assertCreationJob(t, server, "site.restore_clone", clone.ID)
		})
	}
}

func siteByDomain(t *testing.T, server *Server, domain string) model.Site {
	t.Helper()
	sites, err := server.store.ListSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range sites {
		if site.Domain == domain {
			return site
		}
	}
	t.Fatalf("site %q was not persisted", domain)
	return model.Site{}
}

func assertCreationJob(t *testing.T, server *Server, kind, siteID string) {
	t.Helper()
	job, found, err := server.store.ClaimNextJob(context.Background())
	if err != nil || !found || job.Kind != kind || job.TargetID != siteID || job.IdempotencyKey != kind+":"+siteID {
		t.Fatalf("creation job does not use the allocated identity: %#v, found = %t, error = %v", job, found, err)
	}
	if err := server.store.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
}

func siteIDSnapshot(t *testing.T, server *Server, owner store.User, siteID string) string {
	t.Helper()
	ctx := context.Background()
	target, _, err := server.store.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Storage", Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim backup storage fixture: found = %t, error = %v", found, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.EnqueueSiteBackup(ctx, owner, siteID, target.ID); err != nil {
		t.Fatal(err)
	}
	job, found, err = server.store.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim backup fixture: found = %t, error = %v", found, err)
	}
	if err := server.store.FinishJob(ctx, job, `{"snapshot_id":"`+strings.Repeat("a", 64)+`"}`, nil); err != nil {
		t.Fatal(err)
	}
	snapshots, err := server.store.ListSiteSnapshots(ctx, siteID)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("backup fixture: snapshots = %v, error = %v", snapshots, err)
	}
	return snapshots[0].ID
}
