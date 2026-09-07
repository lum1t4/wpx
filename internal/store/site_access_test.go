package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestSetSiteAccessHashesPasswordAndPersistsDesiredState(t *testing.T) {
	ctx := context.Background()
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim provision: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	password := "a-private-access-password"
	jobID, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{BasicAuthEnabled: true, Username: "visitor", CloudflareOnly: true}, password)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := state.SiteAccess(ctx, "access-site")
	if err != nil {
		t.Fatal(err)
	}
	if !settings.BasicAuthEnabled || !settings.CloudflareOnly || settings.Status != "pending" || settings.PasswordHash == "" || strings.Contains(settings.PasswordHash, password) {
		t.Fatalf("unexpected desired settings: %#v", settings)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil || strings.Contains(queued.PayloadJSON, password) || strings.Contains(queued.PayloadJSON, settings.PasswordHash) {
		t.Fatalf("secret leaked into job: job=%#v err=%v", queued, err)
	}
}

func TestSetSiteAccessRequiresAdministrativeSiteAuthority(t *testing.T) {
	ctx := context.Background()
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	job, _, _ := state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	collaborator := User{ID: "collaborator", Role: rbac.Collaborator}
	if _, err := state.SetSiteAccess(ctx, collaborator, "access-site", model.SiteAccessSettings{CloudflareOnly: true}, ""); err == nil {
		t.Fatal("collaborator changed origin access policy")
	}
}

func TestSiteAccessSerializesRapidAndInflightChanges(t *testing.T) {
	ctx := context.Background()
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	provision, _, _ := state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	firstID, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{CloudflareOnly: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{}, ""); err == nil {
		t.Fatal("a rapid second desired state was accepted before the first applied")
	}
	first, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || first.ID != firstID || first.Kind != "site.access_apply" {
		t.Fatalf("claim first access job: job=%#v found=%v err=%v", first, found, err)
	}
	if _, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{}, ""); err == nil {
		t.Fatal("new desired state was accepted while the prior host apply was running")
	}
	if err := state.FinishJob(ctx, first, "{}", errors.New("Nginx rejected settings")); err != nil {
		t.Fatal(err)
	}
	failed, err := state.SiteAccess(ctx, "access-site")
	if err != nil || failed.Status != "failed" || !failed.CloudflareOnly {
		t.Fatalf("failed apply lost desired state: settings=%#v err=%v", failed, err)
	}
	if _, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{}, ""); err != nil {
		t.Fatalf("corrected settings could not be saved after completion: %v", err)
	}
}

func TestCloudflareOnlyConflictsWithEnabledSecurityDefense(t *testing.T) {
	ctx := context.Background()
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	provision, _, _ := state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	now := state.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := state.db.ExecContext(ctx, `INSERT INTO site_security_settings(site_id,enabled,updated_at) VALUES(?,1,?)`, "access-site", now); err != nil {
		t.Fatal(err)
	}
	if _, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{CloudflareOnly: true}, ""); err == nil {
		t.Fatal("Cloudflare-only mode was enabled with socket-peer security defense")
	}
}
