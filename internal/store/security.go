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

// SecurityMigration is appended to migrations by the integration owner. New
// sites deliberately start disabled; enabling firewall behavior requires an
// authenticated, authorized action in the site security page.
const SecurityMigration = `CREATE TABLE site_security_settings (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
	login_protection INTEGER NOT NULL DEFAULT 1 CHECK(login_protection IN (0,1)),
	xmlrpc_protection INTEGER NOT NULL DEFAULT 1 CHECK(xmlrpc_protection IN (0,1)),
	sensitive_path_protection INTEGER NOT NULL DEFAULT 1 CHECK(sensitive_path_protection IN (0,1)),
	burst_404_protection INTEGER NOT NULL DEFAULT 1 CHECK(burst_404_protection IN (0,1)),
	status TEXT NOT NULL DEFAULT 'not_configured' CHECK(status IN ('not_configured','queued','active','failed')),
	last_error TEXT NOT NULL DEFAULT '',
	generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0),
	updated_at TEXT NOT NULL
);`

func (s *Store) SiteSecuritySettings(ctx context.Context, siteID string) (model.SecuritySettings, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return model.SecuritySettings{}, err
	}
	settings := model.DefaultSecuritySettings()
	err := s.db.QueryRowContext(ctx, `SELECT enabled,login_protection,xmlrpc_protection,sensitive_path_protection,burst_404_protection,status,last_error,generation FROM site_security_settings WHERE site_id=?`, siteID).
		Scan(&settings.Enabled, &settings.LoginProtection, &settings.XMLRPCProtection, &settings.SensitivePathProtection, &settings.Burst404Protection, &settings.Status, &settings.LastError, &settings.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	return settings, err
}

func (s *Store) EnqueueSiteSecuritySettings(ctx context.Context, actor User, site model.Site, settings model.SecuritySettings) (string, error) {
	if !s.UserCanSite(ctx, actor, site.ID, rbac.ManageWordPress) {
		return "", errors.New("permission denied")
	}
	if err := model.ValidateSecuritySettings(site, settings); err != nil {
		return "", err
	}
	jobID := mustID("job_")
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := requireSiteIdle(ctx, tx, site.ID); err != nil {
		return "", err
	}
	var currentKind model.SiteKind
	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT kind,status FROM sites WHERE id=?`, site.ID).Scan(&currentKind, &currentStatus); err != nil || currentKind != model.WordPress || currentStatus != "active" {
		return "", errors.New("security settings require an active WordPress site")
	}
	if settings.Enabled {
		var cloudflareOnly bool
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT cloudflare_only FROM site_access_settings WHERE site_id=?),0)`, site.ID).Scan(&cloudflareOnly); err != nil {
			return "", err
		}
		if cloudflareOnly {
			return "", errors.New("disable Cloudflare-only access before enabling WordPress defense")
		}
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='wordpress.security_apply' AND target_type='site' AND target_id=? AND status IN ('queued','running')`, site.ID).Scan(&pending); err != nil {
		return "", err
	}
	if pending != 0 {
		return "", errors.New("a security change is already in progress")
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM site_security_settings WHERE site_id=?`, site.ID).Scan(&generation); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_security_settings(site_id,enabled,login_protection,xmlrpc_protection,sensitive_path_protection,burst_404_protection,status,last_error,generation,updated_at)
		VALUES(?,?,?,?,?,?,'queued','',?,?) ON CONFLICT(site_id) DO UPDATE SET enabled=excluded.enabled,login_protection=excluded.login_protection,xmlrpc_protection=excluded.xmlrpc_protection,sensitive_path_protection=excluded.sensitive_path_protection,burst_404_protection=excluded.burst_404_protection,status='queued',last_error='',generation=excluded.generation,updated_at=excluded.updated_at`,
		site.ID, settings.Enabled, settings.LoginProtection, settings.XMLRPCProtection, settings.SensitivePathProtection, settings.Burst404Protection, generation, now); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(struct {
		Generation int64 `json:"generation"`
	}{generation})
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.security_apply", "site", site.ID, "queued", "waiting", 0, actor.ID, "wordpress.security_apply:"+site.ID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	detail, _ := json.Marshal(settings)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		mustID("aud_"), actor.ID, "wordpress.security_update_requested", "site", site.ID, "success", string(detail), now); err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) finishSecurityApply(ctx context.Context, tx *sql.Tx, job Job, errorText string, operationErr error) error {
	var payload struct {
		Generation int64 `json:"generation"`
	}
	if json.Unmarshal([]byte(job.PayloadJSON), &payload) != nil || payload.Generation < 1 {
		return errors.New("security apply job payload is invalid")
	}
	status := "active"
	if operationErr != nil {
		status = "failed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE site_security_settings SET status=?,last_error=?,updated_at=? WHERE site_id=? AND generation=?`, status, errorText, s.now().UTC().Format(time.RFC3339Nano), job.TargetID, payload.Generation)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("security settings generation changed before job completion")
	}
	return nil
}

func (s *Store) EnqueueSecurityInstall(ctx context.Context, actor User, siteID string) (string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageServer) || model.ValidateSiteID(siteID) != nil {
		return "", errors.New("permission denied")
	}
	jobID := mustID("job_")
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND kind='wordpress' AND status IN ('active','disabled')`, siteID).Scan(&ready); err != nil || ready != 1 {
		return "", errors.New("security installation requires an active or disabled WordPress site")
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='wordpress.security_install' AND status IN ('queued','running')`).Scan(&pending); err != nil {
		return "", err
	}
	if pending != 0 {
		return "", errors.New("security installation is already queued")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.security_install", "server", "security-defense", "queued", "waiting", 0, actor.ID, "wordpress.security_install:"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "wordpress.security_install_requested", "server", "security-defense", "success", now); err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) SecurityInstallStatus(ctx context.Context) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE kind='wordpress.security_install' ORDER BY created_at DESC LIMIT 1`).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

// RetrySecurityJob retains the desired generation and idempotency key after an
// uncertain broker exchange. Replaying converges the same complete files.
func (s *Store) RetrySecurityJob(ctx context.Context, jobID, detail string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind IN ('wordpress.security_apply','wordpress.security_install') AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("security job is not awaiting broker confirmation")
	}
	return nil
}
