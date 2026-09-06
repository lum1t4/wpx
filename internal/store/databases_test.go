package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func activeDatabaseTestSite(t *testing.T, state *Store, owner User) {
	t.Helper()
	if _, err := state.CreateSite(context.Background(), owner, model.Site{ID: "database-site", Domain: "database.example.com", Kind: model.PHP, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(context.Background())
	if err != nil || !found {
		t.Fatalf("claim site job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDatabaseSecretsAndLifecycle(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	activeDatabaseTestSite(t, state, owner)
	public, createJobID, err := state.CreateDatabase(ctx, owner, "database-site", "Application data")
	if err != nil {
		t.Fatal(err)
	}
	if public.Password != "" || public.ID == "" || createJobID == "" {
		t.Fatalf("public database leaked credentials or identifiers: %#v", public)
	}
	stored, err := state.Database(ctx, public.ID)
	if err != nil || stored.Password == "" || stored.Status != "queued" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT config_ciphertext FROM databases WHERE id=?`, public.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(stored.Password)) {
		t.Fatal("database password was stored in plaintext")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.create" || job.ID != createJobID {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueDatabaseDelete(ctx, owner, public.ID, "wrong"); err == nil {
		t.Fatal("database deletion did not require exact confirmation")
	}
	deleteJobID, err := state.EnqueueDatabaseDelete(ctx, owner, public.ID, stored.Name)
	if err != nil || deleteJobID == "" {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.delete" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Database(ctx, public.ID); err == nil {
		t.Fatal("successfully deleted database remained in panel state")
	}
}

func TestDatabaseAdminInstallStatusTracksDurableJob(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if status, err := state.DatabaseAdminStatus(ctx); err != nil || status != "not_installed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := state.EnqueueDatabaseAdminInstall(ctx, owner); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.admin_install" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", errors.New("network unavailable")); err != nil {
		t.Fatal(err)
	}
	if status, err := state.DatabaseAdminStatus(ctx); err != nil || status != "failed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := state.EnqueueDatabaseAdminInstall(ctx, owner); err != nil {
		t.Fatalf("failed installation could not be retried: %v", err)
	}
}
