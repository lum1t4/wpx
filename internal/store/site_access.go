package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"golang.org/x/crypto/bcrypt"
)

const SiteAccessMigration = `
CREATE TABLE IF NOT EXISTS site_access_settings (
    site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    basic_auth_enabled INTEGER NOT NULL DEFAULT 0 CHECK(basic_auth_enabled IN (0,1)),
    username TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    cloudflare_only INTEGER NOT NULL DEFAULT 0 CHECK(cloudflare_only IN (0,1)),
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('pending','active','failed')),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
`

func (s *Store) SiteAccess(ctx context.Context, siteID string) (model.SiteAccessSettings, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return model.SiteAccessSettings{}, err
	}
	settings := model.SiteAccessSettings{SiteID: siteID, Status: "active"}
	err := s.db.QueryRowContext(ctx, `SELECT basic_auth_enabled,username,password_hash,cloudflare_only,status,last_error FROM site_access_settings WHERE site_id=?`, siteID).
		Scan(&settings.BasicAuthEnabled, &settings.Username, &settings.PasswordHash, &settings.CloudflareOnly, &settings.Status, &settings.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if checkErr := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=?`, siteID).Scan(&exists); checkErr != nil {
			return model.SiteAccessSettings{}, checkErr
		}
		if exists != 1 {
			return model.SiteAccessSettings{}, errors.New("site does not exist")
		}
		return settings, nil
	}
	return settings, err
}

// SetSiteAccess commits desired state before host work. A failed apply keeps
// these values and marks them failed in FinishJob so the operator can retry the
// same intent after correcting the host problem.
func (s *Store) SetSiteAccess(ctx context.Context, actor User, siteID string, desired model.SiteAccessSettings, password string) (string, error) {
	if actor.Role != rbac.Owner && actor.Role != rbac.Administrator {
		return "", errors.New("permission denied")
	}
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	desired.SiteID = siteID
	desired.Username = strings.TrimSpace(desired.Username)
	current, err := s.SiteAccess(ctx, siteID)
	if err != nil {
		return "", err
	}
	if current.Status == "pending" {
		return "", errors.New("an access change is already pending")
	}
	desired.PasswordHash = current.PasswordHash
	if password != "" {
		if err := model.ValidateSiteAccessPassword(password); err != nil {
			return "", err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			return "", fmt.Errorf("hash basic authentication password: %w", err)
		}
		desired.PasswordHash = string(hash)
	}
	desired.Status, desired.LastError = "pending", ""
	if err := model.ValidateSiteAccessSettings(desired); err != nil {
		return "", err
	}
	jobID := mustID("job_")
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND status='active'`, siteID).Scan(&ready); err != nil || ready != 1 {
		return "", errors.New("access settings require an active site")
	}
	if desired.CloudflareOnly {
		var defenseEnabled bool
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT enabled FROM site_security_settings WHERE site_id=?),0)`, siteID).Scan(&defenseEnabled); err != nil {
			return "", err
		}
		if defenseEnabled {
			return "", errors.New("turn off WordPress security defense before allowing Cloudflare traffic only")
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO site_access_settings(site_id,basic_auth_enabled,username,password_hash,cloudflare_only,status,last_error,updated_at)
		VALUES(?,?,?,?,?,'pending','',?) ON CONFLICT(site_id) DO UPDATE SET basic_auth_enabled=excluded.basic_auth_enabled,username=excluded.username,password_hash=excluded.password_hash,cloudflare_only=excluded.cloudflare_only,status='pending',last_error='',updated_at=excluded.updated_at
		WHERE site_access_settings.status <> 'pending'`,
		siteID, desired.BasicAuthEnabled, desired.Username, desired.PasswordHash, desired.CloudflareOnly, now)
	if err != nil {
		return "", err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if changed != 1 {
		return "", errors.New("an access change is already pending")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		jobID, "site.access_apply", "site", siteID, "queued", "waiting", 0, actor.ID, "site.access_apply:"+siteID+":"+jobID, now, now); err != nil {
		return "", err
	}
	detail := fmt.Sprintf(`{"basic_auth":%t,"cloudflare_only":%t,"username":%q}`, desired.BasicAuthEnabled, desired.CloudflareOnly, desired.Username)
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		mustID("aud_"), actor.ID, "site.access_requested", "site", siteID, "success", detail, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

// RetrySiteAccessJob preserves both the original job key and the pending row.
// The host apply converges complete managed files, so replay is safe after a
// broker response is lost.
func (s *Store) RetrySiteAccessJob(ctx context.Context, jobID, detail string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind='site.access_apply' AND status='running'`,
		detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("site access job is not awaiting broker confirmation")
	}
	return nil
}
