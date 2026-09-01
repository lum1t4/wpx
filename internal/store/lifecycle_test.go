package store

import (
	"context"
	"errors"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSiteLifecycleAndSharedPHPUsage(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []model.Site{
		{ID: "php-one", Domain: "one.example.com", Kind: model.PHP, PHPVersion: "8.4"},
		{ID: "php-two", Domain: "two.example.com", Kind: model.PHP, PHPVersion: "8.4"},
	} {
		if _, err := state.CreateSite(ctx, owner, site); err != nil {
			t.Fatal(err)
		}
		job, found, err := state.ClaimNextJob(ctx)
		if err != nil || !found {
			t.Fatal("site job unavailable")
		}
		if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, "php-one"); err != nil {
		t.Fatal(err)
	}
	inUse, err := state.OtherActiveSiteUsesPHP(ctx, "php-one", "8.4")
	if err != nil || !inUse {
		t.Fatalf("shared PHP usage=%v err=%v", inUse, err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "site.disable" {
		t.Fatalf("disable job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, "php-two"); err != nil {
		t.Fatal(err)
	}
	inUse, err = state.OtherActiveSiteUsesPHP(ctx, "php-two", "8.4")
	if err != nil || inUse {
		t.Fatalf("last PHP usage=%v err=%v", inUse, err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatal("last disable job unavailable")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteEnable(ctx, owner, "php-one"); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "site.enable" {
		t.Fatalf("enable job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "php-one")
	if err != nil || site.Status != "active" {
		t.Fatalf("site=%#v err=%v", site, err)
	}
}

func TestFailedProvisioningCanBeRetried(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := state.CreateSite(ctx, owner, model.Site{ID: "retry-site", Domain: "retry.example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	job, claimed, err := state.ClaimNextJob(ctx)
	if err != nil || !claimed || job.ID != jobID {
		t.Fatalf("claim=%#v %v %v", job, claimed, err)
	}
	if err := state.FinishJob(ctx, job, "{}", errors.New("temporary host failure")); err != nil {
		t.Fatal(err)
	}
	retryID, err := state.EnqueueSiteProvisionRetry(ctx, owner, "retry-site")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := state.Job(ctx, retryID)
	if err != nil || retry.Kind != "site.provision" || retry.Status != "queued" {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	site, err := state.Site(ctx, "retry-site")
	if err != nil || site.Status != "queued" {
		t.Fatalf("site=%#v err=%v", site, err)
	}
}
