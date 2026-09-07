package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSiteSecuritySettingsAreOptInAndPersistSelection(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, err := state.CreateOwner(context.Background(), "owner", "long-enough-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if _, err := state.CreateSite(context.Background(), owner, site); err != nil {
		t.Fatal(err)
	}
	provision, found, err := state.ClaimNextJob(context.Background())
	if err != nil || !found || provision.Kind != "site.provision" {
		t.Fatalf("claim provision: found=%t job=%+v error=%v", found, provision, err)
	}
	if err := state.FinishJob(context.Background(), provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err = state.Site(context.Background(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := state.SiteSecuritySettings(context.Background(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || !settings.LoginProtection || !settings.XMLRPCProtection || !settings.SensitivePathProtection || !settings.Burst404Protection {
		t.Fatalf("unexpected opt-in defaults: %#v", settings)
	}
	settings.Enabled = true
	settings.XMLRPCProtection = false
	if _, err := state.EnqueueSiteSecuritySettings(context.Background(), owner, site, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteSecuritySettings(context.Background(), owner, site, settings); err == nil {
		t.Fatal("overlapping security change was queued")
	}
	job, found, err := state.ClaimNextJob(context.Background())
	if err != nil || !found || job.Kind != "wordpress.security_apply" {
		t.Fatalf("claim security job: found=%t job=%+v error=%v", found, job, err)
	}
	if err := state.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	stored, err := state.SiteSecuritySettings(context.Background(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || stored.XMLRPCProtection || !stored.LoginProtection || stored.Status != "active" {
		t.Fatalf("stored=%#v", stored)
	}
	if _, err := state.db.Exec(`INSERT INTO site_access_settings(site_id,cloudflare_only,updated_at) VALUES(?,1,CURRENT_TIMESTAMP)`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteSecuritySettings(context.Background(), owner, site, settings); err == nil {
		t.Fatal("security defense was queued behind Cloudflare-only access")
	}
	if _, err := state.db.Exec(`DELETE FROM site_access_settings WHERE site_id=?`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.Exec(`UPDATE sites SET status='disabled' WHERE id=?`, site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteSecuritySettings(context.Background(), owner, site, settings); err == nil {
		t.Fatal("stale active-site snapshot allowed security apply to a disabled site")
	}
}
