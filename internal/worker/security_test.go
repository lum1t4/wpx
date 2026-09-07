package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type securityTestProvisioner struct {
	SiteProvisioner
	apply func(context.Context, model.Site, model.SecuritySettings, string) error
}

func (p securityTestProvisioner) ApplySecurity(ctx context.Context, site model.Site, settings model.SecuritySettings, key string) error {
	return p.apply(ctx, site, settings, key)
}

func securityWorkerFixture(t *testing.T) (*store.Store, store.User, *Worker, model.Site) {
	t.Helper()
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if _, err := state.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision processed=%v error=%v", processed, err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state, owner, w, site
}

func TestWorkerAppliesExactSecurityGeneration(t *testing.T) {
	ctx := context.Background()
	state, owner, w, site := securityWorkerFixture(t)
	settings := model.DefaultSecuritySettings()
	settings.Enabled = true
	settings.XMLRPCProtection = false
	jobID, err := state.EnqueueSiteSecuritySettings(ctx, owner, site, settings)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Provisioner = securityTestProvisioner{SiteProvisioner: &fakeProvisioner{}, apply: func(_ context.Context, gotSite model.Site, got model.SecuritySettings, key string) error {
		calls++
		if gotSite.ID != site.ID || !got.Enabled || got.XMLRPCProtection || got.Status != "queued" || got.Generation != 1 || key != queued.IdempotencyKey {
			t.Fatalf("unexpected apply site=%#v settings=%#v key=%q", gotSite, got, key)
		}
		return nil
	}}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 1 {
		t.Fatalf("processed=%v calls=%d error=%v", processed, calls, err)
	}
	stored, err := state.SiteSecuritySettings(ctx, site.ID)
	if err != nil || stored.Status != "active" || !stored.Enabled || stored.Generation != 1 {
		t.Fatalf("stored=%#v error=%v", stored, err)
	}
}

func TestWorkerRetriesUncertainSecurityApplyWithSameGeneration(t *testing.T) {
	ctx := context.Background()
	state, owner, w, site := securityWorkerFixture(t)
	settings := model.DefaultSecuritySettings()
	settings.Enabled = true
	jobID, err := state.EnqueueSiteSecuritySettings(ctx, owner, site, settings)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Provisioner = securityTestProvisioner{SiteProvisioner: &fakeProvisioner{}, apply: func(_ context.Context, _ model.Site, got model.SecuritySettings, key string) error {
		calls++
		if got.Generation != 1 || key != queued.IdempotencyKey {
			t.Fatalf("retry changed request settings=%#v key=%q", got, key)
		}
		if calls == 1 {
			return errors.Join(broker.ErrOutcomeUnknown, errors.New("lost response"))
		}
		return nil
	}}
	if processed, err := w.ProcessOne(ctx); err == nil || processed {
		t.Fatalf("uncertain processed=%v error=%v", processed, err)
	}
	after, err := state.Job(ctx, jobID)
	if err != nil || after.Status != "queued" || after.PayloadJSON != queued.PayloadJSON || after.IdempotencyKey != queued.IdempotencyKey {
		t.Fatalf("retry job=%#v error=%v", after, err)
	}
	stored, err := state.SiteSecuritySettings(ctx, site.ID)
	if err != nil || stored.Status != "queued" {
		t.Fatalf("desired state released: %#v error=%v", stored, err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 2 {
		t.Fatalf("replay processed=%v calls=%d error=%v", processed, calls, err)
	}
}
