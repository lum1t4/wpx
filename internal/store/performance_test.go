package store

import (
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSetWordPressPerformancePersistsDesiredStateAndQueuesJob(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	provision, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim provisioning: found=%v err=%v", found, err)
	}
	if err := s.FinishJob(ctx, provision, "{}", nil); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.SetWordPressPerformance(ctx, owner, "example-com", false, true)
	if err != nil {
		t.Fatal(err)
	}
	site, err := s.Site(ctx, "example-com")
	if err != nil || site.RedisEnabled || !site.FastCGICacheEnabled {
		t.Fatalf("unexpected desired state: %#v err=%v", site, err)
	}
	job, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID || job.Kind != "wordpress.performance_apply" {
		t.Fatalf("unexpected performance job: %#v found=%v err=%v", job, found, err)
	}
}

func TestSetWordPressPerformanceRejectsInactiveAndNonWordPressSites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, _ := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := s.CreateSite(ctx, owner, model.Site{ID: "queued-wp", Domain: "queued.example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWordPressPerformance(ctx, owner, "queued-wp", true, true); err == nil {
		t.Fatal("inactive WordPress site accepted performance settings")
	}
}
