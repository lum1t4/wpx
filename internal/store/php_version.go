package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

// EnqueuePHPVersionChange leaves the installed version in desired state until
// the host reports a successful switch. Reserving the site and recording the
// job in one transaction prevents two requests from changing the same pool.
func (s *Store) EnqueuePHPVersionChange(ctx context.Context, actor User, siteID, version string, allowEOL bool) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var role rbac.Role
	var disabled bool
	if err := tx.QueryRowContext(ctx, `SELECT role,disabled FROM users WHERE id=?`, actor.ID).Scan(&role, &disabled); err != nil || disabled || !rbac.Allows(role, rbac.ManageAllSites) {
		return "", errors.New("permission denied")
	}
	site, err := phpVersionSite(ctx, tx, siteID)
	if err != nil {
		return "", errors.New("site is unavailable")
	}
	if site.Status != "active" && site.Status != "php_change_failed" {
		return "", errors.New("site must be active to change PHP version")
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE target_type='site' AND target_id=? AND status IN ('queued','running'))`, siteID).Scan(&pending); err != nil {
		return "", err
	}
	if pending {
		return "", errors.New("wait for the site's current operation to finish before changing PHP version")
	}
	change := model.PHPVersionChange{PreviousVersion: site.PHPVersion, Version: version, AllowEOL: allowEOL}
	if err := model.ValidatePHPVersionChange(site, change); err != nil {
		return "", err
	}
	payload, err := json.Marshal(change)
	if err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET status='php_changing',updated_at=? WHERE id=?`, now, siteID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.php_version", "site", siteID, "queued", "waiting", 0, actor.ID, "site.php_version:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "site.php_version.requested", "site", siteID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func phpVersionSite(ctx context.Context, tx *sql.Tx, siteID string) (model.Site, error) {
	var site model.Site
	err := tx.QueryRowContext(ctx, `SELECT id,domain,kind,php_version,upstream,php_eol_ack,status,tls_status,created_at,environment,COALESCE(parent_site_id,''),redis_enabled,fastcgi_cache_enabled,wordpress_multisite FROM sites WHERE id=?`, siteID).
		Scan(&site.ID, &site.Domain, &site.Kind, &site.PHPVersion, &site.Upstream, &site.AllowEOL, &site.Status, &site.TLSStatus, &site.CreatedAt, &site.Environment, &site.ParentSiteID, &site.RedisEnabled, &site.FastCGICacheEnabled, &site.WordPressMultisite)
	return site, err
}

// RetryPHPVersionChange preserves the reservation when the broker may have
// changed the host but its response was lost. Reusing the same job and payload
// lets the privileged operation reconcile its previous attempt before a new
// version request is accepted.
func (s *Store) RetryPHPVersionChange(ctx context.Context, jobID, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind='site.php_version' AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	return err
}

func finishPHPVersionChange(ctx context.Context, tx *sql.Tx, job Job, now string, operationErr error) error {
	if operationErr != nil {
		siteStatus := "php_change_failed"
		var changeErr *model.PHPVersionChangeError
		if errors.As(operationErr, &changeErr) && changeErr.PreviousRestored {
			// A rejected runtime change does not disable a working site. Only
			// the host can confirm that its previous pool is serving again.
			siteStatus = "active"
		}
		_, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=? AND status='php_changing'`, siteStatus, now, job.TargetID)
		return err
	}
	var change model.PHPVersionChange
	if err := json.Unmarshal([]byte(job.PayloadJSON), &change); err != nil {
		return errors.New("PHP version job payload is invalid")
	}
	site, err := phpVersionSite(ctx, tx, job.TargetID)
	if err != nil {
		return err
	}
	if site.Status != "php_changing" || site.PHPVersion != change.PreviousVersion {
		return errors.New("PHP version job no longer matches the site")
	}
	if err := model.ValidatePHPVersionChange(site, change); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE sites SET status='active',php_version=?,php_eol_ack=?,updated_at=? WHERE id=?`, change.Version, change.AllowEOL, now, job.TargetID)
	return err
}
