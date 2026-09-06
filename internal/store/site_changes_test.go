package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func claimLifecycleJob(t *testing.T, state *Store, id string) Job {
	t.Helper()
	job, found, err := state.ClaimNextJob(context.Background())
	if err != nil || !found || job.ID != id {
		t.Fatalf("claim=%#v found=%v err=%v", job, found, err)
	}
	return job
}

func TestDomainChangeReservesDestinationAndCommitsOnlyAfterHost(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.WordPress)
	ctx := context.Background()
	if _, err := state.db.Exec(`UPDATE sites SET tls_status='active' WHERE id='php-site'`); err != nil {
		t.Fatal(err)
	}
	id, err := state.EnqueueDomainChange(ctx, owner, "php-site", "  NEW.Example.com.  ")
	if err != nil {
		t.Fatal(err)
	}
	site, _ := state.Site(ctx, "php-site")
	if site.Status != "domain_changing" || site.Domain != "php.example.com" || site.TLSStatus != "active" {
		t.Fatalf("premature desired-state change: %#v", site)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "other-site", Domain: "new.example.com", Kind: model.Static}); err == nil {
		t.Fatal("creation stole a reserved domain")
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "other-site", Domain: " NEW.EXAMPLE.COM. ", Kind: model.Static}); err == nil {
		t.Fatal("non-normalized creation stole a reserved domain")
	}
	if _, err := state.EnqueueSiteDelete(ctx, owner, site.ID, site.Domain); err == nil {
		t.Fatal("deletion raced a domain change")
	}
	if _, err := state.EnqueueSiteDisable(ctx, owner, site.ID); err == nil {
		t.Fatal("disable raced a domain change")
	}
	job := claimLifecycleJob(t, state, id)
	var change model.DomainChange
	if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil || change.Domain != "new.example.com" || change.PreviousTLSStatus != "active" {
		t.Fatalf("change=%#v err=%v", change, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, _ = state.Site(ctx, site.ID)
	if site.Status != "active" || site.Domain != change.Domain || site.TLSStatus != "not_configured" {
		t.Fatalf("completed site=%#v", site)
	}
	if _, err := state.EnqueueDomainChange(ctx, owner, site.ID, "second.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site, _ = state.Site(ctx, site.ID)
	if site.Status != "domain_changing" || site.Domain != "new.example.com" {
		t.Fatal("late completion overwrote a newer reservation")
	}
}

func TestDomainChangeRecoveryAndInterruptedReplay(t *testing.T) {
	for _, restored := range []bool{true, false} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) {
			state := openTestStore(t)
			owner := activePHPVersionTestSite(t, state, model.Static)
			ctx := context.Background()
			id, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com")
			if err != nil {
				t.Fatal(err)
			}
			job := claimLifecycleJob(t, state, id)
			if n, err := state.RequeueInterruptedJobs(ctx); err != nil || n != 1 {
				t.Fatalf("requeue=%d err=%v", n, err)
			}
			replay := claimLifecycleJob(t, state, id)
			if replay.PayloadJSON != job.PayloadJSON || replay.IdempotencyKey != job.IdempotencyKey {
				t.Fatal("restart changed domain request")
			}
			if err := state.FinishJob(ctx, replay, "{}", &model.DomainChangeError{Err: errors.New("host change failed"), PreviousRestored: restored}); err != nil {
				t.Fatal(err)
			}
			site, _ := state.Site(ctx, "php-site")
			if site.Domain != "php.example.com" {
				t.Fatal("failed host change committed destination")
			}
			if restored {
				if site.Status != "active" {
					t.Fatalf("restored site=%#v", site)
				}
				if _, err := state.CreateSite(ctx, owner, model.Site{ID: "other-site", Domain: "new.example.com", Kind: model.Static}); err != nil {
					t.Fatalf("confirmed rollback retained reservation: %v", err)
				}
				return
			}
			if site.Status != "domain_change_failed" {
				t.Fatalf("unrecovered site=%#v", site)
			}
			if _, err := state.EnqueueDomainChange(ctx, owner, site.ID, "third.example.com"); err == nil {
				t.Fatal("new request replaced unrecovered journal")
			}
			if _, err := state.CreateSite(ctx, owner, model.Site{ID: "other-site", Domain: "new.example.com", Kind: model.Static}); err == nil {
				t.Fatal("unrecovered destination became available")
			}
			retryID, err := state.RetryDomainChange(ctx, owner, site.ID)
			if err != nil || retryID != id {
				t.Fatalf("retry=%q err=%v", retryID, err)
			}
			retry := claimLifecycleJob(t, state, id)
			if retry.PayloadJSON != job.PayloadJSON || retry.IdempotencyKey != job.IdempotencyKey {
				t.Fatal("explicit retry changed original request")
			}
			if err := state.FinishJob(ctx, retry, "{}", nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLifecycleChangesCheckCurrentRoleAndConfirmation(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.Static)
	ctx := context.Background()
	for _, role := range []rbac.Role{rbac.Administrator, rbac.Collaborator, rbac.Customer} {
		if _, err := state.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, owner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com"); err == nil {
			t.Fatalf("stale owner authorized deletion as %s", role)
		}
		if role != rbac.Administrator {
			if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
				t.Fatalf("stale owner authorized domain change as %s", role)
			}
		}
	}
	if _, err := state.db.Exec(`UPDATE users SET role='owner',disabled=1 WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com"); err == nil {
		t.Fatal("disabled owner authorized deletion")
	}
	if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
		t.Fatal("disabled owner authorized domain change")
	}
	if _, err := state.db.Exec(`UPDATE users SET disabled=0 WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	for _, confirmation := range []string{"", "php-site", "PHP.example.com", "php.example.com "} {
		if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", confirmation); err == nil {
			t.Fatalf("accepted incorrect confirmation %q", confirmation)
		}
	}
	if _, err := state.db.Exec(`UPDATE users SET role='administrator' WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	owner.Role = rbac.Customer
	if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err != nil {
		t.Fatalf("current administrator could not change domain: %v", err)
	}
}

func TestLifecycleReservationCoversRelatedJobs(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.Static)
	ctx := context.Background()
	for _, payload := range []string{`{"source_id":"php-site"}`, `{"staging_id":"php-site"}`} {
		id := mustID("job_")
		if _, err := state.db.Exec(`INSERT INTO jobs(id,kind,target_type,target_id,status,idempotency_key,payload_json,created_at,updated_at) VALUES(?,'site.restore_clone','site','other-site','queued',?,?,'now','now')`, id, id, payload); err != nil {
			t.Fatal(err)
		}
		if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
			t.Fatal("domain change ignored a source/staging reference")
		}
		if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com"); err == nil {
			t.Fatal("deletion ignored a source/staging reference")
		}
		if _, err := state.db.Exec(`UPDATE jobs SET status='succeeded' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"source_id":"php-site"}`, `{"staging_id":"php-site"}`} {
		id := mustID("job_")
		if _, err := state.db.Exec(`INSERT INTO jobs(id,kind,target_type,target_id,status,idempotency_key,payload_json,created_at,updated_at) VALUES(?,'site.restore_clone','site','other-site','queued',?,?,'now','now')`, id, id, payload); err == nil {
			t.Fatal("new related job bypassed reserved site lock")
		}
	}
}

func TestDomainReservationIsExclusive(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.Static)
	ctx := context.Background()
	id, err := state.CreateSite(ctx, owner, model.Site{ID: "second-site", Domain: "second.example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	if err := state.FinishJob(ctx, claimLifecycleJob(t, state, id), "{}", nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, siteID := range []string{"php-site", "second-site"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := state.EnqueueDomainChange(ctx, owner, siteID, "new.example.com")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("reservation winners=%d", succeeded)
	}
}

func TestDomainChangeRejectsUnsafeRequestsAndReservedPHPRetainsRuntime(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.WordPress)
	ctx := context.Background()
	for _, domain := range []string{"", "php.example.com", "https://new.example.com", "new.example.com/path", "*.example.com", "new.example.com;id"} {
		if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", domain); err == nil {
			t.Fatalf("invalid domain %q accepted", domain)
		}
	}
	for _, status := range []string{"disabled", "failed", "queued", "php_changing", "deleting", "domain_change_failed"} {
		if _, err := state.db.Exec(`UPDATE sites SET status=? WHERE id='php-site'`, status); err != nil {
			t.Fatal(err)
		}
		if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
			t.Fatalf("unsafe status %q accepted", status)
		}
	}
	if _, err := state.db.Exec(`UPDATE sites SET status='active',wordpress_multisite='subdomains' WHERE id='php-site'`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
		t.Fatal("multisite domain change accepted without network migration")
	}
	for _, status := range []string{"domain_changing", "domain_change_failed", "deleting", "delete_failed"} {
		if _, err := state.db.Exec(`UPDATE sites SET status=? WHERE id='php-site'`, status); err != nil {
			t.Fatal(err)
		}
		inUse, err := state.OtherActiveSiteUsesPHP(ctx, "neighbor-site", "8.4")
		if err != nil || !inUse {
			t.Fatalf("reserved PHP ignored for status=%s inUse=%v err=%v", status, inUse, err)
		}
	}
}

func seedDeletionAssociations(t *testing.T, state *Store, siteID, ownerID string) {
	t.Helper()
	for _, query := range []string{
		`INSERT INTO backup_targets VALUES('storage','Storage','s3','active',X'01','now','now')`,
		`INSERT INTO backup_snapshots VALUES('snapshot','` + siteID + `','storage','01234567','now')`,
		`INSERT INTO backup_schedules(id,site_id,target_id,interval_hours,next_run,created_at,updated_at) VALUES('schedule','` + siteID + `','storage',24,'2000-01-01','now','now')`,
		`INSERT INTO dns_providers VALUES('provider','Provider','cloudflare','active',X'02','now','now')`,
		`INSERT INTO dns_records(id,site_id,provider_id,name,type,value,ttl,status,created_at,updated_at) VALUES('record','` + siteID + `','provider','php.example.com','A','192.0.2.1',300,'active','now','now')`,
		`INSERT INTO staging_credentials VALUES('` + siteID + `','wpx',X'03')`,
		`INSERT INTO site_snippets VALUES('` + siteID + `','','','now')`,
		`INSERT INTO site_grants VALUES('` + ownerID + `','` + siteID + `','view_site')`,
	} {
		if _, err := state.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeletePreservesEvidenceAndSnapshotsUntilHostConfirmation(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.PHP)
	ctx := context.Background()
	seedDeletionAssociations(t, state, "php-site", owner.ID)
	id, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := state.EnqueueDueBackups(ctx); err != nil || n != 0 {
		t.Fatalf("deletion enqueued scheduled backup: %d %v", n, err)
	}
	schedules, err := state.ListSiteBackupSchedules(ctx, "php-site")
	if err != nil || len(schedules) != 1 || schedules[0].Enabled {
		t.Fatalf("schedules=%#v err=%v", schedules, err)
	}
	job := claimLifecycleJob(t, state, id)
	if err := state.FinishJob(ctx, job, "{}", errors.New("host deletion interrupted")); err != nil {
		t.Fatal(err)
	}
	site, err := state.Site(ctx, "php-site")
	if err != nil || site.Status != "delete_failed" {
		t.Fatalf("failed site=%#v err=%v", site, err)
	}
	if snapshots, err := state.ListSiteSnapshots(ctx, site.ID); err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots prematurely removed: %#v %v", snapshots, err)
	}
	retryID, err := state.EnqueueSiteDelete(ctx, owner, site.ID, site.Domain)
	if err != nil || retryID != id {
		t.Fatalf("retry=%q err=%v", retryID, err)
	}
	retry := claimLifecycleJob(t, state, id)
	if retry.PayloadJSON != job.PayloadJSON || retry.IdempotencyKey != job.IdempotencyKey {
		t.Fatal("deletion retry changed destructive identity")
	}
	if err := state.FinishJob(ctx, retry, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Site(ctx, site.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted site still exists: %v", err)
	}
	for _, table := range []string{"site_grants", "site_snippets", "staging_credentials", "backup_schedules", "dns_records"} {
		var count int
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("association %s count=%d err=%v", table, count, err)
		}
	}
	for _, table := range []string{"deleted_sites", "backup_snapshots", "backup_targets", "dns_providers"} {
		var count int
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("retained %s count=%d err=%v", table, count, err)
		}
	}
	completed, err := state.Job(ctx, id)
	if err != nil || completed.Status != "succeeded" {
		t.Fatalf("job history=%#v err=%v", completed, err)
	}
	if err := state.FinishJob(ctx, retry, "{}", nil); err != nil {
		t.Fatalf("duplicate completion: %v", err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: site.ID, Domain: "replacement.example.com", Kind: model.Static}); err == nil {
		t.Fatal("deleted identity was reused despite retained snapshots/journals")
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "new-identity", Domain: site.Domain, Kind: model.Static}); err != nil {
		t.Fatalf("new identity cannot reuse released domain: %v", err)
	}
}

func TestDeleteRejectsStagingChildrenAndPendingDNS(t *testing.T) {
	state := openTestStore(t)
	owner := activePHPVersionTestSite(t, state, model.WordPress)
	ctx := context.Background()
	if _, err := state.db.Exec(`INSERT INTO sites(id,domain,kind,php_version,status,environment,parent_site_id,created_at,updated_at) VALUES('child-site','child.example.com','wordpress','8.4','disabled','staging','php-site','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com"); err == nil {
		t.Fatal("production deletion bypassed staging child")
	}
	if _, err := state.db.Exec(`DELETE FROM sites WHERE id='child-site'`); err != nil {
		t.Fatal(err)
	}
	seedDeletionAssociations(t, state, "php-site", owner.ID)
	if _, err := state.db.Exec(`INSERT INTO jobs(id,kind,target_type,target_id,status,idempotency_key,created_at,updated_at) VALUES('dns-job','dns.record_apply','dns_record','record','running','dns-key','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueSiteDelete(ctx, owner, "php-site", "php.example.com"); err == nil {
		t.Fatal("deletion raced pending DNS operation")
	}
	if _, err := state.EnqueueDomainChange(ctx, owner, "php-site", "new.example.com"); err == nil {
		t.Fatal("domain change raced pending DNS operation")
	}
}

func TestLifecycleMigrationPreservesExistingSnapshotRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for index, migration := range migrations[:len(migrations)-1] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, index+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO sites(id,domain,kind,status,created_at,updated_at) VALUES('legacy-site','legacy.example.com','static','active','now','now')`,
		`INSERT INTO backup_targets VALUES('storage','Storage','s3','active',X'01','now','now')`,
		`INSERT INTO backup_snapshots VALUES('snapshot','legacy-site','storage','01234567','now')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.db.Exec(`DELETE FROM sites WHERE id='legacy-site'`); err != nil {
		t.Fatal(err)
	}
	snapshots, err := state.ListSiteSnapshots(context.Background(), "legacy-site")
	if err != nil || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != "01234567" {
		t.Fatalf("migrated snapshots=%#v err=%v", snapshots, err)
	}
	rows, err := state.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a broken foreign key")
	}
}
