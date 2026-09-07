package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestCronScheduleCRUDAndSiteAuthorization(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "cron-owner", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, siteID := range []string{"cron-site", "other-site"} {
		if _, err := state.CreateSite(ctx, owner, model.Site{ID: siteID, Domain: siteID + ".example.com", Kind: model.Static}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		job, found, err := state.ClaimNextJob(ctx)
		if err != nil || !found {
			t.Fatalf("claim site fixture: %v", err)
		}
		if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
			t.Fatal(err)
		}
	}
	collaborator, err := state.CreateUser(ctx, owner, "cron-collaborator", "strong-test-password", rbac.Collaborator, []string{"cron-site"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateCronSchedule(ctx, collaborator, model.CronSchedule{SiteID: "cron-site", Name: "blocked", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true}); err == nil {
		t.Fatal("collaborator created an owner-only schedule")
	}
	schedule, err := state.CreateCronSchedule(ctx, owner, model.CronSchedule{SiteID: "cron-site", Name: "queue", Expression: "*/5 * * * *", Command: []string{"php", "artisan", "queue:work"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if schedule.ApplyStatus != "pending" {
		t.Fatalf("apply status = %q", schedule.ApplyStatus)
	}
	applyFailure := errors.New("cron daemon unavailable")
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "site.cron_apply" {
		t.Fatalf("claim cron job: %#v %v", job, err)
	}
	if err := state.FinishJob(ctx, job, "{}", applyFailure); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.CronSchedule(ctx, schedule.SiteID, schedule.ID)
	if err != nil || loaded.ApplyStatus != "failed" || loaded.ApplyError != applyFailure.Error() {
		t.Fatalf("failed result not durable: %+v %v", loaded, err)
	}
	loaded.Name, loaded.Expression = "queue hourly", "0 * * * *"
	if err := state.UpdateCronSchedule(ctx, owner, loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = state.CronSchedule(ctx, schedule.SiteID, schedule.ID)
	if loaded.ApplyStatus != "pending" {
		t.Fatalf("updated status = %q", loaded.ApplyStatus)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim update: %v", err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	deleting, err := state.RequestCronScheduleDelete(ctx, owner, schedule.SiteID, schedule.ID)
	if err != nil || deleting.ID != schedule.ID {
		t.Fatalf("request delete: %+v %v", deleting, err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim delete: %v", err)
	}
	if err := state.FinishJob(ctx, job, "{}", applyFailure); err != nil {
		t.Fatal(err)
	}
	loaded, _ = state.CronSchedule(ctx, schedule.SiteID, schedule.ID)
	if loaded.ApplyStatus != "failed" {
		t.Fatalf("failed deletion lost desired state: %+v", loaded)
	}
	if _, err := state.RequestCronScheduleDelete(ctx, owner, schedule.SiteID, schedule.ID); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim retry delete: %v", err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CronSchedule(ctx, schedule.SiteID, schedule.ID); err == nil {
		t.Fatal("schedule remains after confirmed host deletion")
	}
}

func TestWordPressCronSettingDefaultsAndPersistsPendingIntent(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "wp-cron-owner", "strong-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "wordpress-cron", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	setting, err := state.WordPressCronSetting(ctx, "wordpress-cron")
	if err != nil || setting.Replaced || setting.Expression != "*/5 * * * *" {
		t.Fatalf("default = %+v, %v", setting, err)
	}
	setting.Replaced, setting.Expression = true, "*/10 * * * *"
	if err := state.SetWordPressCronSetting(ctx, owner, setting); err != nil {
		t.Fatal(err)
	}
	setting, _ = state.WordPressCronSetting(ctx, "wordpress-cron")
	if !setting.Replaced || setting.ApplyStatus != "pending" {
		t.Fatalf("saved setting = %+v", setting)
	}
}
