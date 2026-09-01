package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestBackupTargetSecretsAreEncryptedAndInitializationIsDurable(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	public, generatedPassword, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Primary", Endpoint: "https://objects.example.com", Bucket: "customer-backups",
		Prefix: "server-one", Region: "eu-west-1", BucketLookup: "path",
		AccessKey: "sensitive-access", SecretKey: "sensitive-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if generatedPassword == "" || public.AccessKey != "" || public.SecretKey != "" || public.RepositoryPassword != "" {
		t.Fatalf("unexpected public target or password: %#v %q", public, generatedPassword)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, "SELECT config_ciphertext FROM backup_targets WHERE id=?", public.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-access", "sensitive-secret", generatedPassword} {
		if bytes.Contains(ciphertext, []byte(secret)) {
			t.Fatalf("secret %q was stored as plaintext", secret)
		}
	}
	stored, err := state.BackupTarget(ctx, public.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessKey != "sensitive-access" || stored.RepositoryPassword != generatedPassword {
		t.Fatal("encrypted target did not round-trip")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "backup.target_init" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	targets, err := state.ListBackupTargets(ctx)
	if err != nil || len(targets) != 1 || targets[0].Status != "active" {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
}

func TestGoogleDriveTargetSecretsAreEncrypted(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	token := `{"access_token":"short-lived","refresh_token":"sensitive-refresh-token","token_type":"Bearer"}`
	public, password, err := state.CreateGoogleDriveTarget(ctx, owner, model.BackupTarget{
		Name: "Drive", DriveFolder: "WPX Backups", GoogleClientID: "123-example.apps.googleusercontent.com",
		GoogleClientSecret: "sensitive-client-secret", GoogleToken: token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if password == "" || public.GoogleToken != "" || public.GoogleClientSecret != "" {
		t.Fatalf("unexpected public Drive target: %#v", public)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT config_ciphertext FROM backup_targets WHERE id=?`, public.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-refresh-token", "sensitive-client-secret", password} {
		if bytes.Contains(ciphertext, []byte(secret)) {
			t.Fatalf("Drive secret %q was stored as plaintext", secret)
		}
	}
}

func TestBackupRetentionReconcilesLocalRestorePoints(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{5}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}); err != nil {
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
	ids := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	for _, id := range ids {
		if _, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID); err != nil {
			t.Fatal(err)
		}
		job, _, _ = state.ClaimNextJob(ctx)
		result := `{"snapshot_id":"` + id + `"}`
		if err := state.FinishJob(ctx, job, result, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID); err != nil {
		t.Fatal(err)
	}
	job, _, _ = state.ClaimNextJob(ctx)
	kept := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	result := `{"snapshot_id":"` + kept + `","retention_applied":true,"retained_snapshot_ids":["` + kept + `"]}`
	if err := state.FinishJob(ctx, job, result, nil); err != nil {
		t.Fatal(err)
	}
	snapshots, err := state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != kept {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
}
