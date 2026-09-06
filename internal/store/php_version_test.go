package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func activePHPVersionTestSite(t *testing.T, state *Store, kind model.SiteKind) User {
	t.Helper()
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "php-site", Domain: "php.example.com", Kind: kind}
	if kind == model.WordPress || kind == model.PHP {
		site.PHPVersion = "8.4"
	}
	if _, err := state.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("provision job found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestPHPVersionChangeRetainsInstalledVersionUntilSuccess(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.WordPress)
	ctx := context.Background()
	jobID, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false)
	if err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "php-site")
	if err != nil || site.PHPVersion != "8.4" || site.Status != "php_changing" {
		t.Fatalf("queued site=%#v err=%v", site, err)
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, site.ID); err == nil {
		t.Fatal("disable was allowed while PHP change was queued")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID || job.Kind != "site.php_version" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	var change model.PHPVersionChange
	if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil || change.PreviousVersion != "8.4" || change.Version != "8.5" || change.AllowEOL {
		t.Fatalf("change=%#v err=%v", change, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil || site.PHPVersion != "8.5" || site.Status != "active" || !site.RedisEnabled || !site.FastCGICacheEnabled {
		t.Fatalf("completed site=%#v err=%v", site, err)
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, site.ID, "8.5", false); err == nil {
		t.Fatal("current version was accepted")
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, site.ID, "8.3", false); err != nil {
		t.Fatal(err)
	}
	// Repeated completion from an old worker must not release the next job's
	// reservation or replace its source version.
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil || site.PHPVersion != "8.5" || site.Status != "php_changing" {
		t.Fatalf("repeated completion changed site=%#v err=%v", site, err)
	}
}

func TestPHPVersionFailureCanBeRetriedWithEOLAcknowledgement(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "7.4", false); err == nil {
		t.Fatal("EOL branch was accepted without acknowledgement")
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "7.4", true); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("job found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", errors.New("PHP configuration rejected")); err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "php-site")
	if err != nil || site.PHPVersion != "8.4" || site.AllowEOL || site.Status != "php_change_failed" {
		t.Fatalf("failed site=%#v err=%v", site, err)
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, site.ID, "7.4", true); err != nil {
		t.Fatal(err)
	}
	retry, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || retry.IdempotencyKey == job.IdempotencyKey {
		t.Fatalf("retry=%#v found=%v err=%v", retry, found, err)
	}
	if err := state.FinishJob(ctx, retry, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil || site.PHPVersion != "7.4" || !site.AllowEOL || site.Status != "active" {
		t.Fatalf("retried site=%#v err=%v", site, err)
	}
	// An older failure cannot undo the successful retry.
	if err := state.FinishJob(ctx, job, "{}", errors.New("late failure")); err != nil {
		t.Fatal(err)
	}
	site, err = state.Site(ctx, site.ID)
	if err != nil || site.PHPVersion != "7.4" || site.Status != "active" {
		t.Fatalf("late failure changed site=%#v err=%v", site, err)
	}
}

func TestPHPVersionChangeChecksCurrentActorPermissions(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		role     rbac.Role
		disabled bool
	}{
		{name: "collaborator", role: rbac.Collaborator},
		{name: "customer", role: rbac.Customer},
		{name: "disabled owner", role: rbac.Owner, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := state.db.ExecContext(ctx, `UPDATE users SET role=?,disabled=? WHERE id=?`, tc.role, tc.disabled, owner.ID); err != nil {
				t.Fatal(err)
			}
			// The request still claims owner rights; the database is authoritative.
			if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err == nil {
				t.Fatal("unauthorized actor was accepted")
			}
		})
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, User{ID: "missing", Role: rbac.Owner}, "php-site", "8.5", false); err == nil {
		t.Fatal("unknown actor was accepted")
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE users SET role='administrator',disabled=0 WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	owner.Role = rbac.Customer
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err != nil {
		t.Fatalf("current administrator was rejected: %v", err)
	}
}

func TestPHPVersionChangeRestoresManageabilityAfterVerifiedRollback(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	jobID, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false)
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	operationErr := &model.PHPVersionChangeError{Err: errors.New("target configuration rejected"), PreviousRestored: true}
	if err := state.FinishJob(ctx, job, "{}", operationErr); err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "php-site")
	if err != nil || site.Status != "active" || site.PHPVersion != "8.4" {
		t.Fatalf("restored site=%#v err=%v", site, err)
	}
	finished, err := state.Job(ctx, jobID)
	if err != nil || finished.Status != "failed" || finished.Error != operationErr.Error() {
		t.Fatalf("failed switch was hidden: job=%#v err=%v", finished, err)
	}
	if _, err := state.SetSiteSnippets(ctx, owner, site.ID, model.SiteSnippets{}); err != nil {
		t.Fatalf("restored site cannot be managed: %v", err)
	}
}

func TestPHPVersionChangeRejectsUnavailableSitesAndInvalidVersions(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.Static)
	ctx := context.Background()
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err == nil {
		t.Fatal("static site accepted PHP change")
	}
	if _, err := state.db.ExecContext(ctx, `UPDATE sites SET kind='php',php_version='8.4' WHERE id='php-site'`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"8.4", "", "9.0", "8.4; id"} {
		if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", version, false); err == nil {
			t.Fatalf("invalid or current version %q was accepted", version)
		}
	}
	for _, status := range []string{"disabled", "queued", "enabling", "disabling", "failed"} {
		if _, err := state.db.ExecContext(ctx, `UPDATE sites SET status=? WHERE id='php-site'`, status); err != nil {
			t.Fatal(err)
		}
		if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err == nil {
			t.Fatalf("site status %q was accepted", status)
		}
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "missing-site", "8.5", false); err == nil {
		t.Fatal("missing site was accepted")
	}
}

func TestPHPVersionChangeSerializesConcurrentRequests(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	var requests sync.WaitGroup
	results := make(chan error, 12)
	start := make(chan struct{})
	for range cap(results) {
		requests.Add(1)
		go func() {
			defer requests.Done()
			<-start
			_, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false)
			results <- err
		}()
	}
	close(start)
	requests.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("accepted %d concurrent requests, want 1", succeeded)
	}
	var queued int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='site.php_version' AND status='queued'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
}

func TestPHPVersionChangeWaitsForOtherSiteJobs(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	if _, err := state.SetSiteSnippets(ctx, owner, "php-site", model.SiteSnippets{}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err == nil {
		t.Fatal("PHP change was accepted alongside a queued config operation")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("config job found=%v err=%v", found, err)
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err == nil {
		t.Fatal("PHP change was accepted alongside a running config operation")
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueuePHPVersionChange(ctx, owner, "php-site", "8.5", false); err != nil {
		t.Fatal(err)
	}
}

func TestPHPUsageIncludesChangingAndFailedSites(t *testing.T) {
	state := openTestStore(t)
	activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	for _, status := range []string{"php_changing", "php_change_failed"} {
		if _, err := state.db.ExecContext(ctx, `UPDATE sites SET status=? WHERE id='php-site'`, status); err != nil {
			t.Fatal(err)
		}
		inUse, err := state.OtherActiveSiteUsesPHP(ctx, "another-site", "8.4")
		if err != nil || !inUse {
			t.Fatalf("status=%s inUse=%v err=%v", status, inUse, err)
		}
	}
}
