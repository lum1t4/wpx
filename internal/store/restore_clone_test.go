package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestRestoreCloneCreatesProtectedStagingDestinationAtomically(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
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
	if _, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _ = state.ClaimNextJob(ctx)
	snapshotID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := state.FinishJob(ctx, job, `{"snapshot_id":"`+snapshotID+`"}`, nil); err != nil {
		t.Fatal(err)
	}
	snapshots, _ := state.ListSiteSnapshots(ctx, "example-com")
	jobID, password, err := state.EnqueueRestoreClone(ctx, owner, "example-com", snapshots[0].ID, "restored-stage", "restored.example.com", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 24 {
		t.Fatal("one-time staging password was not generated")
	}
	staging, err := state.Site(ctx, "restored-stage")
	if err != nil || staging.Environment != "staging" || staging.ParentSiteID != "example-com" || staging.Status != "queued" {
		t.Fatalf("staging=%#v err=%v", staging, err)
	}
	username, decrypted, err := state.StagingCredential(ctx, staging.ID)
	if err != nil || username != "wpx" || decrypted != password {
		t.Fatalf("credential user=%q matches=%v err=%v", username, decrypted == password, err)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT password_ciphertext FROM staging_credentials WHERE site_id=?`, staging.ID).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte(password)) {
		t.Fatal("staging password was not encrypted at rest")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID || job.Kind != "site.restore_clone" || job.TargetID != staging.ID {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
}
