package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type lifecycleTestProvisioner struct {
	SiteProvisioner
	change func(context.Context, model.Site, model.DomainChange, string) error
	delete func(context.Context, model.Site, bool, string) error
}

func (p lifecycleTestProvisioner) ChangeDomain(ctx context.Context, site model.Site, change model.DomainChange, key string) error {
	return p.change(ctx, site, change, key)
}

func (p lifecycleTestProvisioner) DeleteSite(ctx context.Context, site model.Site, stopPHP bool, key string) error {
	return p.delete(ctx, site, stopPHP, key)
}

func lifecycleWorkerFixture(t *testing.T) (*store.Store, store.User, *Worker, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "test-site", Domain: "test.example.com", Kind: model.PHP, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision processed=%v err=%v", processed, err)
	}
	return state, owner, w, path
}

func TestWorkerDomainChangeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		operationErr  error
		wantDomain    string
		wantStatus    string
		wantJobStatus string
	}{
		{name: "success", wantDomain: "new.example.com", wantStatus: "active", wantJobStatus: "succeeded"},
		{name: "confirmed recovery", operationErr: &model.DomainChangeError{Err: errors.New("new vhost rejected"), PreviousRestored: true}, wantDomain: "test.example.com", wantStatus: "active", wantJobStatus: "failed"},
		{name: "unrecovered failure", operationErr: errors.New("host change failed"), wantDomain: "test.example.com", wantStatus: "domain_change_failed", wantJobStatus: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state, owner, w, _ := lifecycleWorkerFixture(t)
			id, err := state.EnqueueDomainChange(ctx, owner, "test-site", "new.example.com")
			if err != nil {
				t.Fatal(err)
			}
			queued, err := state.Job(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			w.Provisioner = lifecycleTestProvisioner{SiteProvisioner: &fakeProvisioner{}, change: func(_ context.Context, site model.Site, change model.DomainChange, key string) error {
				calls++
				if site.Domain != "test.example.com" || site.Status != "domain_changing" || change.PreviousDomain != site.Domain || change.Domain != "new.example.com" || key != queued.IdempotencyKey {
					t.Fatalf("broker domain request=%#v change=%#v key=%q", site, change, key)
				}
				return tc.operationErr
			}}
			if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 1 {
				t.Fatalf("processed=%v calls=%d err=%v", processed, calls, err)
			}
			site, err := state.Site(ctx, "test-site")
			if err != nil || site.Domain != tc.wantDomain || site.Status != tc.wantStatus {
				t.Fatalf("site=%#v err=%v", site, err)
			}
			job, err := state.Job(ctx, id)
			if err != nil || job.Status != tc.wantJobStatus {
				t.Fatalf("job=%#v err=%v", job, err)
			}
		})
	}
}

func TestWorkerLifecycleUnknownOutcomeReplaysIdenticalRequest(t *testing.T) {
	for _, kind := range []string{"domain", "delete"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			state, owner, w, _ := lifecycleWorkerFixture(t)
			var id string
			var err error
			if kind == "domain" {
				id, err = state.EnqueueDomainChange(ctx, owner, "test-site", "new.example.com")
			} else {
				id, err = state.EnqueueSiteDelete(ctx, owner, "test-site", "test.example.com")
			}
			if err != nil {
				t.Fatal(err)
			}
			queued, err := state.Job(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			calls, request := 0, ""
			reply := func(payload any, key string) error {
				calls++
				encoded, _ := json.Marshal(payload)
				if calls == 1 {
					request = string(encoded)
				}
				if key != queued.IdempotencyKey || string(encoded) != request {
					t.Fatal("retry changed broker key or payload")
				}
				if calls == 1 {
					return errors.Join(broker.ErrOutcomeUnknown, errors.New("lost reply"))
				}
				if calls == 2 {
					return errors.Join(broker.ErrUnavailable, errors.New("restarting"))
				}
				return nil
			}
			w.Provisioner = lifecycleTestProvisioner{
				SiteProvisioner: &fakeProvisioner{},
				change: func(_ context.Context, site model.Site, change model.DomainChange, key string) error {
					return reply(broker.ChangeDomainRequest{Site: site, Change: change}, key)
				},
				delete: func(_ context.Context, site model.Site, stopPHP bool, key string) error {
					return reply(broker.DeleteSiteRequest{Site: site, StopPHP: stopPHP}, key)
				},
			}
			for range 2 {
				if processed, err := w.ProcessOne(ctx); err == nil || processed {
					t.Fatalf("uncertain processed=%v err=%v", processed, err)
				}
				job, err := state.Job(ctx, id)
				if err != nil || job.Status != "queued" || job.PayloadJSON != queued.PayloadJSON || job.IdempotencyKey != queued.IdempotencyKey {
					t.Fatalf("retry job=%#v err=%v", job, err)
				}
				site, err := state.Site(ctx, "test-site")
				if err != nil || site.Domain != "test.example.com" || (site.Status != "domain_changing" && site.Status != "deleting") {
					t.Fatalf("uncertain reservation=%#v err=%v", site, err)
				}
			}
			if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 3 {
				t.Fatalf("reconcile processed=%v calls=%d err=%v", processed, calls, err)
			}
			job, err := state.Job(ctx, id)
			if err != nil || job.Status != "succeeded" {
				t.Fatalf("completion=%#v err=%v", job, err)
			}
			if kind == "delete" {
				if _, err := state.Site(ctx, "test-site"); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("site not removed after confirmation: %v", err)
				}
			}
		})
	}
}

func TestWorkerDeletionFailureResumesOriginalPayload(t *testing.T) {
	ctx := context.Background()
	state, owner, w, _ := lifecycleWorkerFixture(t)
	id, err := state.EnqueueSiteDelete(ctx, owner, "test-site", "test.example.com")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Provisioner = lifecycleTestProvisioner{SiteProvisioner: &fakeProvisioner{}, delete: func(_ context.Context, site model.Site, stopPHP bool, key string) error {
		calls++
		if site.Status != "deleting" || key != queued.IdempotencyKey || !stopPHP {
			t.Fatalf("deletion request=%#v stop=%v key=%q", site, stopPHP, key)
		}
		if calls == 1 {
			return errors.New("host deletion interrupted")
		}
		return nil
	}}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("first processed=%v err=%v", processed, err)
	}
	site, err := state.Site(ctx, "test-site")
	if err != nil || site.Status != "delete_failed" {
		t.Fatalf("site=%#v err=%v", site, err)
	}
	retryID, err := state.EnqueueSiteDelete(ctx, owner, site.ID, site.Domain)
	if err != nil || retryID != id {
		t.Fatalf("retry=%q err=%v", retryID, err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 2 {
		t.Fatalf("retry processed=%v calls=%d err=%v", processed, calls, err)
	}
}

func TestWorkerLifecycleRejectsMalformedOrStaleRequestsBeforeHost(t *testing.T) {
	for _, kind := range []string{"domain", "delete"} {
		for _, mutation := range []string{"malformed", "stale"} {
			t.Run(kind+"/"+mutation, func(t *testing.T) {
				ctx := context.Background()
				state, owner, w, path := lifecycleWorkerFixture(t)
				var id string
				var err error
				if kind == "domain" {
					id, err = state.EnqueueDomainChange(ctx, owner, "test-site", "new.example.com")
				} else {
					id, err = state.EnqueueSiteDelete(ctx, owner, "test-site", "test.example.com")
				}
				if err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if mutation == "malformed" {
					_, err = db.Exec(`UPDATE jobs SET payload_json='{' WHERE id=?`, id)
				} else {
					_, err = db.Exec(`UPDATE sites SET domain='stale.example.com' WHERE id='test-site'`)
				}
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
				w.Provisioner = lifecycleTestProvisioner{SiteProvisioner: &fakeProvisioner{}, change: func(context.Context, model.Site, model.DomainChange, string) error {
					t.Fatal("invalid domain request reached host")
					return nil
				}, delete: func(context.Context, model.Site, bool, string) error {
					t.Fatal("invalid deletion request reached host")
					return nil
				}}
				if processed, err := w.ProcessOne(ctx); err != nil || !processed {
					t.Fatalf("processed=%v err=%v", processed, err)
				}
				job, err := state.Job(ctx, id)
				if err != nil || job.Status != "failed" {
					t.Fatalf("job=%#v err=%v", job, err)
				}
			})
		}
	}
}
