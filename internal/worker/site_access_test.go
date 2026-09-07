package worker

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type accessWorkerProvisioner struct {
	SiteProvisioner
	settings model.SiteAccessSettings
	calls    int
}

func (p *accessWorkerProvisioner) ApplySiteAccess(_ context.Context, _ model.Site, settings model.SiteAccessSettings, _ string) error {
	p.calls++
	p.settings = settings
	return nil
}

func TestWorkerAppliesPersistedSiteAccessDesiredState(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	base := &fakeProvisioner{}
	w := Worker{Store: state, Provisioner: base, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision processed=%v err=%v", processed, err)
	}
	if _, err := state.SetSiteAccess(ctx, owner, "access-site", model.SiteAccessSettings{CloudflareOnly: true}, ""); err != nil {
		t.Fatal(err)
	}
	access := &accessWorkerProvisioner{SiteProvisioner: base}
	w.Provisioner = access
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("access processed=%v err=%v", processed, err)
	}
	if access.calls != 1 || !access.settings.CloudflareOnly || access.settings.SiteID != "access-site" {
		t.Fatalf("host did not receive desired settings: calls=%d settings=%#v", access.calls, access.settings)
	}
	persisted, err := state.SiteAccess(ctx, "access-site")
	if err != nil || persisted.Status != "active" || !persisted.CloudflareOnly {
		t.Fatalf("successful state=%#v err=%v", persisted, err)
	}
}
