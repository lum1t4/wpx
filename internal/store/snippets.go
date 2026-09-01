package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) SiteSnippets(ctx context.Context, siteID string) (model.SiteSnippets, error) {
	var snippets model.SiteSnippets
	err := s.db.QueryRowContext(ctx, `SELECT nginx,php FROM site_snippets WHERE site_id=?`, siteID).Scan(&snippets.Nginx, &snippets.PHP)
	if err == sql.ErrNoRows {
		return model.SiteSnippets{}, nil
	}
	return snippets, err
}

func (s *Store) SetSiteSnippets(ctx context.Context, actor User, siteID string, snippets model.SiteSnippets) (string, error) {
	site, err := s.Site(ctx, siteID)
	if err != nil || (site.Status != "active" && site.Status != "disabled") {
		return "", errors.New("site must be active or disabled to change expert configuration")
	}
	if err := model.ValidateSiteSnippets(site, snippets); err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_snippets(site_id,nginx,php,updated_at) VALUES(?,?,?,?) ON CONFLICT(site_id) DO UPDATE SET nginx=excluded.nginx,php=excluded.php,updated_at=excluded.updated_at`, site.ID, snippets.Nginx, snippets.PHP, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.config_apply", "site", site.ID, "queued", "waiting", 0, actor.ID, "site.config_apply:"+site.ID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "site.config.requested", "site", site.ID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}
