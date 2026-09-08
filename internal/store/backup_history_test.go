package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSiteBackupRunHistoryTracksLifecycleSourceAndRestoreAvailability(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "history-site", Domain: "history.example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	job, _, _ := state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{Name: "History storage", Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ = state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	state.now = func() time.Time { return started }
	if _, err := state.EnqueueSiteBackup(ctx, owner, "history-site", target.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := state.ListSiteBackupRuns(ctx, "history-site", 50)
	if err != nil || len(runs) != 1 || runs[0].Status != "queued" || runs[0].Source != "Manual" || runs[0].SnapshotID != "" || runs[0].Duration != "" {
		t.Fatalf("queued runs=%#v err=%v", runs, err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim=%#v %v %v", job, found, err)
	}
	runs, err = state.ListSiteBackupRuns(ctx, "history-site", 50)
	if err != nil || runs[0].Status != "running" || runs[0].StartedAt == "" || runs[0].Duration != "<1s" || runs[0].SnapshotID != "" {
		t.Fatalf("running runs=%#v err=%v", runs, err)
	}
	state.now = func() time.Time { return started.Add(65 * time.Second) }
	snapshotID := strings.Repeat("a", 64)
	if err := state.FinishJob(ctx, job, `{"snapshot_id":"`+snapshotID+`"}`, nil); err != nil {
		t.Fatal(err)
	}
	runs, err = state.ListSiteBackupRuns(ctx, "history-site", 50)
	if err != nil || runs[0].Status != "succeeded" || runs[0].SnapshotID != snapshotID || !runs[0].RestorePointAvailable || runs[0].Duration != "1m 5s" {
		t.Fatalf("successful runs=%#v err=%v", runs, err)
	}
	if _, err := state.db.Exec(`DELETE FROM backup_snapshots WHERE site_id=?`, "history-site"); err != nil {
		t.Fatal(err)
	}
	runs, _ = state.ListSiteBackupRuns(ctx, "history-site", 50)
	if runs[0].Status != "succeeded" || runs[0].RestorePointAvailable {
		t.Fatalf("retention rewrote run outcome: %#v", runs[0])
	}

	if _, err := state.EnqueueSiteBackup(ctx, owner, "history-site", target.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _ = state.ClaimNextJob(ctx)
	if err := state.FinishJob(ctx, job, "{}", errors.New("provider leaked https://secret.example/token")); err != nil {
		t.Fatal(err)
	}
	runs, err = state.ListSiteBackupRuns(ctx, "history-site", 50)
	if err != nil || runs[0].Status != "failed" || runs[0].FailureSummary != "Backup failed" || strings.Contains(runs[0].FailureSummary, "secret") {
		t.Fatalf("failed runs=%#v err=%v", runs, err)
	}

	if err := state.SetBackupSchedule(ctx, owner, "history-site", target.ID, 24, model.BackupRetention{KeepDaily: 7}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.Exec(`UPDATE backup_schedules SET next_run=?`, started.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if count, err := state.EnqueueDueBackups(ctx); err != nil || count != 1 {
		t.Fatalf("due=%d err=%v", count, err)
	}
	runs, err = state.ListSiteBackupRuns(ctx, "history-site", 50)
	var scheduled *model.BackupRun
	for index := range runs {
		if runs[index].Source == "Scheduled" {
			scheduled = &runs[index]
			break
		}
	}
	if err != nil || scheduled == nil || scheduled.Status != "queued" || scheduled.SnapshotID != "" || scheduled.ScheduledFor != started.Format(time.RFC3339Nano) {
		t.Fatalf("scheduled runs=%#v err=%v", runs, err)
	}
	payload := `{"target_id":"` + target.ID + `"}`
	for _, fixture := range []struct{ id, key string }{
		{"legacy-unknown-backup", "legacy.backup:history-site:1"},
		{"legacy-manual-backup", "site.backup:history-site:legacy-manual"},
	} {
		if _, err := state.db.Exec(`INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, fixture.id, "site.backup", "site", "history-site", "queued", "waiting", 0, fixture.key, payload, started.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	runs, err = state.ListSiteBackupRuns(ctx, "history-site", 50)
	if err != nil {
		t.Fatal(err)
	}
	sources := make(map[string]string)
	for _, run := range runs {
		sources[run.JobID] = run.Source
	}
	if sources["legacy-unknown-backup"] != "Unknown" || sources["legacy-manual-backup"] != "Manual" {
		t.Fatalf("legacy source inference = %#v", sources)
	}
	other, err := state.ListSiteBackupRuns(ctx, "another-site", 50)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-site history=%#v err=%v", other, err)
	}
}
