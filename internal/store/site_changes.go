package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

// SiteDeletion freezes the broker request before the first attempt. Host work
// can finish after the caller loses its connection; retries must use precisely
// the same identity and key rather than reconstructing a destructive request.
type SiteDeletion struct {
	Site    model.Site `json:"site"`
	StopPHP bool       `json:"stop_php"`
}

func normalizedSiteDomain(domain string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
}

func siteLifecycleActor(ctx context.Context, tx *sql.Tx, actorID string, ownerOnly bool) error {
	var role rbac.Role
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, actorID).Scan(&role, &disabled); err != nil || disabled || (ownerOnly && role != rbac.Owner) || (!ownerOnly && !rbac.Allows(role, rbac.ManageAllSites)) {
		return errors.New("permission denied")
	}
	return nil
}

// A staging deploy targets production but reads staging; a clone targets the
// new site but reads its source. Checking only jobs.target_id would therefore
// allow a domain change or deletion to race a job which still uses this site.
func siteHasPendingJobs(ctx context.Context, tx *sql.Tx, siteID string) (bool, error) {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j WHERE j.status IN ('queued','running') AND (
		(j.target_type='site' AND j.target_id=?) OR
		(j.target_type='dns_record' AND EXISTS(SELECT 1 FROM dns_records d WHERE d.id=j.target_id AND d.site_id=?)) OR
		(json_valid(j.payload_json) AND (json_extract(j.payload_json,'$.source_id')=? OR json_extract(j.payload_json,'$.staging_id')=?))
	))`, siteID, siteID, siteID, siteID).Scan(&pending)
	return pending, err
}

func requireSiteIdle(ctx context.Context, tx *sql.Tx, siteID string) error {
	pending, err := siteHasPendingJobs(ctx, tx, siteID)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("wait for all operations involving this site to finish")
	}
	return nil
}

// EnqueueDomainChange reserves the destination without changing sites.domain.
// SQLite commits the new hostname only after the host has rewritten the vhost
// and, for WordPress, its URLs. The old certificate is never assumed to cover
// the new hostname. DNS remains an explicit, separate operator decision.
func (s *Store) EnqueueDomainChange(ctx context.Context, actor User, siteID, domain string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	domain = normalizedSiteDomain(domain)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := siteLifecycleActor(ctx, tx, actor.ID, false); err != nil {
		return "", err
	}
	site, err := phpVersionSite(ctx, tx, siteID)
	if err != nil {
		return "", errors.New("site is unavailable")
	}
	if site.Status != "active" {
		return "", errors.New("site must be active to change its domain")
	}
	if err := requireSiteIdle(ctx, tx, site.ID); err != nil {
		return "", err
	}
	change := model.DomainChange{PreviousDomain: site.Domain, Domain: domain, PreviousTLSStatus: site.TLSStatus}
	// The host validates the reserved state, not the state before enqueueing.
	site.Status = "domain_changing"
	if err := model.ValidateDomainChange(site, change); err != nil {
		return "", err
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sites WHERE lower(rtrim(trim(domain),'.'))=?) OR EXISTS(SELECT 1 FROM site_domain_reservations WHERE domain=?)`, domain, domain).Scan(&taken); err != nil {
		return "", err
	}
	if taken {
		return "", errors.New("domain is already used or reserved by another site")
	}
	payload, err := json.Marshal(change)
	if err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	if err := enqueueLifecycleJob(ctx, tx, actor, siteID, jobID, "site.domain_change", string(payload), "domain_changing", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_domain_reservations(domain,site_id,job_id) VALUES(?,?,?)`, domain, siteID, jobID); err != nil {
		return "", fmt.Errorf("reserve domain: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func enqueueLifecycleJob(ctx context.Context, tx *sql.Tx, actor User, siteID, jobID, kind, payload, status, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, status, now, siteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, kind, "site", siteID, "queued", "waiting", 0, actor.ID, kind+":"+siteID+":"+jobID, payload, now, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, kind+".requested", "site", siteID, "success", payload, now)
	return err
}

func (s *Store) RetryDomainChange(ctx context.Context, actor User, siteID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := siteLifecycleActor(ctx, tx, actor.ID, false); err != nil {
		return "", err
	}
	jobID, err := retryFailedLifecycleJob(ctx, tx, actor, siteID, "site.domain_change", "domain_change_failed", "domain_changing", s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

// An unrecovered change must reconcile its original journal. Creating a new
// job here would strand the old reservation and could apply a second change
// over host state whose first operation has not yet been acknowledged.
func retryFailedLifecycleJob(ctx context.Context, tx *sql.Tx, actor User, siteID, kind, failedStatus, pendingStatus, now string) (string, error) {
	if err := requireSiteIdle(ctx, tx, siteID); err != nil {
		return "", err
	}
	var status, jobID string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sites WHERE id=?`, siteID).Scan(&status); err != nil || status != failedStatus {
		return "", errors.New("site has no failed operation to retry")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE target_type='site' AND target_id=? AND kind=? AND status='failed' ORDER BY created_at DESC,id DESC LIMIT 1`, siteID, kind).Scan(&jobID); err != nil {
		return "", errors.New("failed site operation is unavailable")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',progress=0,error='',finished_at=NULL,updated_at=? WHERE id=?`, now, jobID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, pendingStatus, now, siteID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, kind+".retried", "site", siteID, "success", fmt.Sprintf(`{"job_id":%q}`, jobID), now); err != nil {
		return "", err
	}
	return jobID, nil
}

// EnqueueSiteDelete requires the current database owner, not a role copied
// into a stale browser session. Schedules are paused with the reservation;
// associations are removed only after the host confirms deletion. A failed
// deletion remains resumable with the same immutable request and job key.
func (s *Store) EnqueueSiteDelete(ctx context.Context, actor User, siteID, confirmationDomain string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := siteLifecycleActor(ctx, tx, actor.ID, true); err != nil {
		return "", err
	}
	site, err := phpVersionSite(ctx, tx, siteID)
	if err != nil {
		return "", errors.New("site is unavailable")
	}
	if confirmationDomain != site.Domain {
		return "", errors.New("type the site's complete domain to confirm deletion")
	}
	if err := requireSiteIdle(ctx, tx, siteID); err != nil {
		return "", err
	}
	var children bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sites WHERE parent_site_id=?)`, siteID).Scan(&children); err != nil {
		return "", err
	}
	if children {
		return "", errors.New("delete this site's staging copies first")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if site.Status == "delete_failed" {
		jobID, err := retryFailedLifecycleJob(ctx, tx, actor, siteID, "site.delete", "delete_failed", "deleting", now)
		if err != nil {
			return "", err
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return jobID, nil
	}
	switch site.Status {
	case "active", "disabled", "failed", "disable_failed", "enable_failed", "php_change_failed":
	default:
		return "", errors.New("site cannot be deleted while another change needs to finish")
	}
	var phpInUse bool
	if site.Kind == model.PHP || site.Kind == model.WordPress {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sites WHERE id<>? AND kind IN ('php','wordpress') AND php_version=? AND status NOT IN ('disabled','failed','delete_failed'))`, siteID, site.PHPVersion).Scan(&phpInUse); err != nil {
			return "", err
		}
	}
	site.Status = "deleting"
	payload, err := json.Marshal(SiteDeletion{Site: site, StopPHP: !phpInUse})
	if err != nil {
		return "", err
	}
	jobID := mustID("job_")
	if err := enqueueLifecycleJob(ctx, tx, actor, siteID, jobID, "site.delete", string(payload), "deleting", now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET enabled=0,updated_at=? WHERE site_id=?`, now, siteID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

// RetrySiteLifecycleJob is used only after an uncertain broker exchange. It
// retains the site/domain reservation and original serialized request.
func (s *Store) RetrySiteLifecycleJob(ctx context.Context, jobID, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind IN ('site.domain_change','site.delete') AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	return err
}

func finishDomainChange(ctx context.Context, tx *sql.Tx, job Job, now string, operationErr error) error {
	if operationErr != nil {
		status := "domain_change_failed"
		var changeErr *model.DomainChangeError
		if errors.As(operationErr, &changeErr) && changeErr.PreviousRestored {
			status = "active"
			if _, err := tx.ExecContext(ctx, `DELETE FROM site_domain_reservations WHERE site_id=? AND job_id=?`, job.TargetID, job.ID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=? AND status='domain_changing'`, status, now, job.TargetID)
		return err
	}
	var change model.DomainChange
	if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil {
		return errors.New("domain change job payload is invalid")
	}
	site, err := phpVersionSite(ctx, tx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "domain_changing" {
		return errors.New("domain change job no longer matches the site's reserved state")
	}
	if err := model.ValidateDomainChange(site, change); err != nil {
		return err
	}
	var reserved bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM site_domain_reservations WHERE site_id=? AND job_id=? AND domain=?)`, site.ID, job.ID, change.Domain).Scan(&reserved); err != nil || !reserved {
		return errors.New("domain change reservation no longer matches the job")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET domain=?,tls_status='not_configured',status='active',updated_at=? WHERE id=?`, change.Domain, now, site.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM site_domain_reservations WHERE site_id=? AND job_id=?`, site.ID, job.ID)
	return err
}

func finishSiteDelete(ctx context.Context, tx *sql.Tx, job Job, now string, operationErr error) error {
	if operationErr != nil {
		_, err := tx.ExecContext(ctx, `UPDATE sites SET status='delete_failed',updated_at=? WHERE id=? AND status='deleting'`, now, job.TargetID)
		return err
	}
	var payload SiteDeletion
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil || payload.Site.ID != job.TargetID {
		return errors.New("site deletion job payload is invalid")
	}
	site, err := phpVersionSite(ctx, tx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "deleting" || site.Domain != payload.Site.Domain || site.Kind != payload.Site.Kind {
		return errors.New("site deletion job no longer matches the site")
	}
	encoded, err := json.Marshal(site)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deleted_sites(id,domain,site_json,deleted_at) VALUES(?,?,?,?)`, site.ID, site.Domain, string(encoded), now); err != nil {
		return err
	}
	// Site-scoped grants, snippets, credentials, DNS associations and schedules
	// have ON DELETE CASCADE. Jobs and audit events intentionally have no site
	// foreign key. Snapshot records retain the deleted ID, and provider secrets
	// remain available; no remote deletion is implied by removing this row.
	_, err = tx.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, site.ID)
	return err
}
