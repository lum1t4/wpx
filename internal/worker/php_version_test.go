package worker

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type phpVersionTestProvisioner struct {
	SiteProvisioner
	change func(context.Context, model.Site, model.PHPVersionChange, string) error
}

func (p phpVersionTestProvisioner) ChangePHPVersion(ctx context.Context, site model.Site, change model.PHPVersionChange, key string) error {
	return p.change(ctx, site, change, key)
}

func TestWorkerPHPVersionChange(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mutation       string
		operationErr   error
		unavailable    bool
		wantCalls      int
		wantJobStatus  string
		wantSiteStatus string
		wantVersion    string
	}{
		{name: "success", wantCalls: 1, wantJobStatus: "succeeded", wantSiteStatus: "active", wantVersion: "8.5"},
		{name: "host failure", operationErr: errors.New("configuration check failed"), wantCalls: 1, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "verified rollback", operationErr: &model.PHPVersionChangeError{Err: errors.New("new PHP failed to start"), PreviousRestored: true}, wantCalls: 1, wantJobStatus: "failed", wantSiteStatus: "active", wantVersion: "8.4"},
		{name: "failed rollback", operationErr: &model.PHPVersionChangeError{Err: errors.New("previous PHP failed to restart")}, wantCalls: 1, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "unavailable", unavailable: true, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "malformed payload", mutation: `UPDATE jobs SET payload_json='{' WHERE kind='site.php_version'`, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "stale previous version", mutation: `UPDATE jobs SET payload_json='{"previous_version":"8.3","version":"8.5"}' WHERE kind='site.php_version'`, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "missing requested version", mutation: `UPDATE jobs SET payload_json='{"previous_version":"8.4"}' WHERE kind='site.php_version'`, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "unacknowledged EOL", mutation: `UPDATE jobs SET payload_json='{"previous_version":"8.4","version":"7.4"}' WHERE kind='site.php_version'`, wantJobStatus: "failed", wantSiteStatus: "php_change_failed", wantVersion: "8.4"},
		{name: "stale site status", mutation: `UPDATE sites SET status='disabled' WHERE id='php-site'`, wantJobStatus: "failed", wantSiteStatus: "disabled", wantVersion: "8.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			state, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := state.CreateSite(ctx, owner, model.Site{ID: "php-site", Domain: "php.example.com", Kind: model.PHP, PHPVersion: "8.4"}); err != nil {
				t.Fatal(err)
			}
			w := Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			if processed, err := w.ProcessOne(ctx); err != nil || !processed {
				t.Fatalf("provision processed=%v err=%v", processed, err)
			}
			jobID, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false)
			if err != nil {
				t.Fatal(err)
			}
			job, err := state.Job(ctx, jobID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			if !tc.unavailable {
				w.Provisioner = phpVersionTestProvisioner{SiteProvisioner: &fakeProvisioner{}, change: func(ctx context.Context, site model.Site, change model.PHPVersionChange, key string) error {
					calls++
					if site.PHPVersion != "8.4" || site.Status != "php_changing" || change.PreviousVersion != "8.4" || change.Version != "8.5" || change.AllowEOL || key != job.IdempotencyKey {
						t.Fatalf("unexpected change: site=%#v change=%#v key=%q", site, change, key)
					}
					stored, err := state.Site(ctx, site.ID)
					if err != nil || stored.PHPVersion != "8.4" || stored.Status != "php_changing" {
						t.Fatalf("database changed before host finished: site=%#v err=%v", stored, err)
					}
					return tc.operationErr
				}}
			}
			if tc.mutation != "" {
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, tc.mutation); err != nil {
					db.Close()
					t.Fatal(err)
				}
				db.Close()
			}
			if processed, err := w.ProcessOne(ctx); err != nil || !processed {
				t.Fatalf("change processed=%v err=%v", processed, err)
			}
			job, err = state.Job(ctx, jobID)
			if err != nil || job.Status != tc.wantJobStatus || calls != tc.wantCalls {
				t.Fatalf("job=%#v calls=%d err=%v", job, calls, err)
			}
			site, err := state.Site(ctx, "php-site")
			if err != nil || site.PHPVersion != tc.wantVersion || site.Status != tc.wantSiteStatus {
				t.Fatalf("site=%#v err=%v", site, err)
			}
			if processed, err := w.ProcessOne(ctx); err != nil || processed || calls != tc.wantCalls {
				t.Fatalf("completed job was replayed: processed=%v calls=%d err=%v", processed, calls, err)
			}
		})
	}
}

func TestWorkerPHPVersionChangeReplaysUncertainOutcome(t *testing.T) {
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "php-site", Domain: "php.example.com", Kind: model.PHP, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision processed=%v err=%v", processed, err)
	}
	jobID, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Provisioner = phpVersionTestProvisioner{SiteProvisioner: &fakeProvisioner{}, change: func(_ context.Context, site model.Site, change model.PHPVersionChange, key string) error {
		calls++
		if site.PHPVersion != "8.4" || change.PreviousVersion != "8.4" || change.Version != "8.5" || key != queued.IdempotencyKey {
			t.Fatalf("retry changed request: site=%#v change=%#v key=%q", site, change, key)
		}
		switch calls {
		case 1:
			return errors.Join(broker.ErrOutcomeUnknown, errors.New("response connection lost"))
		case 2:
			return errors.Join(broker.ErrUnavailable, errors.New("broker restarting"))
		default:
			return nil
		}
	}}
	for attempt := range 2 {
		if processed, err := w.ProcessOne(ctx); err == nil || processed {
			t.Fatalf("attempt %d should yield for retry: processed=%v err=%v", attempt, processed, err)
		}
		job, err := state.Job(ctx, jobID)
		if err != nil || job.Status != "queued" || job.IdempotencyKey != queued.IdempotencyKey || job.PayloadJSON != queued.PayloadJSON {
			t.Fatalf("retry job=%#v err=%v", job, err)
		}
		site, err := state.Site(ctx, "php-site")
		if err != nil || site.Status != "php_changing" || site.PHPVersion != "8.4" {
			t.Fatalf("uncertain site was released: site=%#v err=%v", site, err)
		}
		if _, err := state.EnqueuePHPVersionChange(ctx, owner, site.ID, "8.3", false); err == nil {
			t.Fatal("a different switch was accepted before the first outcome was known")
		}
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed || calls != 3 {
		t.Fatalf("reconciliation processed=%v calls=%d err=%v", processed, calls, err)
	}
	job, err := state.Job(ctx, jobID)
	if err != nil || job.Status != "succeeded" {
		t.Fatalf("reconciled job=%#v err=%v", job, err)
	}
	site, err := state.Site(ctx, "php-site")
	if err != nil || site.Status != "active" || site.PHPVersion != "8.5" {
		t.Fatalf("reconciled site=%#v err=%v", site, err)
	}
}
