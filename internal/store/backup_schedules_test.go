package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestDueBackupScheduleAdvancesAtomicallyAndDoesNotDuplicate(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return current }
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Storage", Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ = state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if err := state.SetBackupSchedule(ctx, owner, "example-com", target.ID, 24, model.BackupRetention{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}, 30); err != nil {
		t.Fatal(err)
	}
	current = current.Add(25 * time.Hour)
	if count, err := state.EnqueueDueBackups(ctx); err != nil || count != 1 {
		t.Fatalf("due count=%d err=%v", count, err)
	}
	if count, err := state.EnqueueDueBackups(ctx); err != nil || count != 0 {
		t.Fatalf("duplicate due count=%d err=%v", count, err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "site.backup" || job.TargetID != "example-com" {
		t.Fatalf("scheduled job=%#v found=%v err=%v", job, found, err)
	}
	var payload struct {
		Retention model.BackupRetention `json:"retention"`
	}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.Retention.KeepDaily != 7 || payload.Retention.KeepWeekly != 4 || payload.Retention.KeepMonthly != 6 {
		t.Fatalf("scheduled retention was not persisted in the job: %s err=%v", job.PayloadJSON, err)
	}
	snapshotID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := state.FinishJob(ctx, job, `{"snapshot_id":"`+snapshotID+`"}`, nil); err != nil {
		t.Fatal(err)
	}
	current = current.Add(31 * 24 * time.Hour)
	if count, err := state.EnqueueDueRestoreTests(ctx); err != nil || count != 1 {
		t.Fatalf("due restore tests=%d err=%v", count, err)
	}
	if count, err := state.EnqueueDueRestoreTests(ctx); err != nil || count != 0 {
		t.Fatalf("duplicate restore tests=%d err=%v", count, err)
	}
	restoreJob, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || restoreJob.Kind != "site.restore_test" {
		t.Fatalf("restore job=%#v found=%v err=%v", restoreJob, found, err)
	}
	if err := state.FinishJob(ctx, restoreJob, "{}", nil); err != nil {
		t.Fatal(err)
	}
	schedules, err := state.ListSiteBackupSchedules(ctx, "example-com")
	if err != nil || len(schedules) != 1 || schedules[0].LastRestoreTestStatus != "passed" || schedules[0].LastRestoreTestAt == "" {
		t.Fatalf("schedules=%#v err=%v", schedules, err)
	}
}
