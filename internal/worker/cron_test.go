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

type cronWorkerProvisioner struct {
	SiteProvisioner
	cron      func(context.Context, model.Site, model.CronSchedule, bool, string) error
	wordpress func(context.Context, model.Site, model.WordPressCronSetting, string) error
}

func (p cronWorkerProvisioner) ApplyCron(ctx context.Context, site model.Site, schedule model.CronSchedule, remove bool, key string) error {
	return p.cron(ctx, site, schedule, remove, key)
}
func (p cronWorkerProvisioner) ApplyWordPressCron(ctx context.Context, site model.Site, setting model.WordPressCronSetting, key string) error {
	return p.wordpress(ctx, site, setting, key)
}

func cronWorkerFixture(t *testing.T, kind model.SiteKind) (*store.Store, store.User, model.Site, Worker) {
	t.Helper()
	ctx := context.Background()
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	owner, err := state.CreateOwner(ctx, "cron-worker-owner", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "cron-worker-site", Domain: "cron.example.com", Kind: kind}
	if kind == model.WordPress {
		site.PHPVersion = "8.4"
	}
	if _, err := state.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("finish site fixture: %v %v", processed, err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	return state, owner, site, w
}

func TestWorkerAppliesPersistedCronAndFinishesDesiredState(t *testing.T) {
	ctx := context.Background()
	state, owner, site, w := cronWorkerFixture(t, model.Static)
	schedule, err := state.CreateCronSchedule(ctx, owner, model.CronSchedule{SiteID: site.ID, Name: "task", Expression: "*/5 * * * *", Command: []string{"php", "task"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := state.PendingCronJob(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobBefore, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Provisioner = cronWorkerProvisioner{SiteProvisioner: &fakeProvisioner{}, cron: func(_ context.Context, gotSite model.Site, got model.CronSchedule, remove bool, key string) error {
		calls++
		if gotSite.ID != site.ID || got.ID != schedule.ID || remove || key != jobBefore.IdempotencyKey {
			t.Fatalf("dispatch site=%#v schedule=%#v remove=%v key=%q", gotSite, got, remove, key)
		}
		return nil
	}, wordpress: func(context.Context, model.Site, model.WordPressCronSetting, string) error { return nil }}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process=%v err=%v", processed, err)
	}
	jobAfter, _ := state.Job(ctx, jobID)
	stored, _ := state.CronSchedule(ctx, site.ID, schedule.ID)
	if calls != 1 || jobAfter.Status != "succeeded" || stored.ApplyStatus != "applied" {
		t.Fatalf("calls=%d job=%#v schedule=%#v", calls, jobAfter, stored)
	}
}

func TestWorkerRetriesUnknownCronOutcomeWithSameKey(t *testing.T) {
	ctx := context.Background()
	state, owner, site, w := cronWorkerFixture(t, model.Static)
	_, err := state.CreateCronSchedule(ctx, owner, model.CronSchedule{SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := state.PendingCronJob(ctx, site.ID)
	original, _ := state.Job(ctx, jobID)
	var keys []string
	w.Provisioner = cronWorkerProvisioner{SiteProvisioner: &fakeProvisioner{}, cron: func(_ context.Context, _ model.Site, _ model.CronSchedule, _ bool, key string) error {
		keys = append(keys, key)
		if len(keys) == 1 {
			return errors.Join(broker.ErrOutcomeUnknown, errors.New("reply lost"))
		}
		return nil
	}, wordpress: func(context.Context, model.Site, model.WordPressCronSetting, string) error { return nil }}
	if processed, err := w.ProcessOne(ctx); err == nil || processed {
		t.Fatalf("unknown outcome processed=%v err=%v", processed, err)
	}
	retry, _ := state.Job(ctx, jobID)
	if retry.Status != "queued" || retry.IdempotencyKey != original.IdempotencyKey {
		t.Fatalf("retry=%#v", retry)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("replay processed=%v err=%v", processed, err)
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys=%v", keys)
	}
}

func TestWorkerAppliesWordPressCronReplacement(t *testing.T) {
	ctx := context.Background()
	state, owner, site, w := cronWorkerFixture(t, model.WordPress)
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/10 * * * *"}
	if err := state.SetWordPressCronSetting(ctx, owner, setting); err != nil {
		t.Fatal(err)
	}
	called := false
	w.Provisioner = cronWorkerProvisioner{SiteProvisioner: &fakeProvisioner{}, cron: func(context.Context, model.Site, model.CronSchedule, bool, string) error { return nil }, wordpress: func(_ context.Context, gotSite model.Site, got model.WordPressCronSetting, key string) error {
		called = true
		if gotSite.ID != site.ID || !got.Replaced || got.Expression != "*/10 * * * *" || key == "" {
			t.Fatalf("wordpress dispatch=%#v %#v %q", gotSite, got, key)
		}
		return nil
	}}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("process=%v err=%v", processed, err)
	}
	stored, _ := state.WordPressCronSetting(ctx, site.ID)
	if !called || stored.ApplyStatus != "applied" {
		t.Fatalf("called=%v setting=%#v", called, stored)
	}
}
