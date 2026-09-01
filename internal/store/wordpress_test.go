package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestEnqueueWordPressUpdateRequiresReadyRecoveryStorage(t *testing.T) {
	s := openTestStore(t)
	if err := s.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := s.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	provision, _, _ := s.ClaimNextJob(ctx)
	if err := s.FinishJob(ctx, provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	target, _, err := s.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Storage", Endpoint: "https://objects.example.com", Bucket: "wpx", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueWordPressUpdate(ctx, owner, "example-com", target.ID, model.WordPressUpdate{Component: model.WordPressCore}); err == nil {
		t.Fatal("unverified backup target accepted for update")
	}
	verify, _, _ := s.ClaimNextJob(ctx)
	if err := s.FinishJob(ctx, verify, "{}", nil); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.EnqueueWordPressUpdate(ctx, owner, "example-com", target.ID, model.WordPressUpdate{Component: model.WordPressPlugin, Name: "akismet"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID || job.Kind != "wordpress.update" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
}
