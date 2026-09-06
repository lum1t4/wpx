package store

import (
	"context"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) EnqueueSiteDisable(ctx context.Context, actor User, siteID string) (string, error) {
	return s.enqueueSiteTransition(ctx, actor, siteID, "site.disable", []string{"active", "disable_failed"}, "disabling")
}

func (s *Store) EnqueueSiteEnable(ctx context.Context, actor User, siteID string) (string, error) {
	return s.enqueueSiteTransition(ctx, actor, siteID, "site.enable", []string{"disabled", "enable_failed"}, "enabling")
}

func (s *Store) EnqueueSiteProvisionRetry(ctx context.Context, actor User, siteID string) (string, error) {
	return s.enqueueSiteTransition(ctx, actor, siteID, "site.provision", []string{"failed"}, "queued")
}

func (s *Store) enqueueSiteTransition(ctx context.Context, actor User, siteID, kind string, allowed []string, transition string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	jobID, now := mustID("job_"), s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sites WHERE id=?`, siteID).Scan(&status); err != nil {
		return "", errors.New("site is unavailable")
	}
	permitted := false
	for _, candidate := range allowed {
		permitted = permitted || status == candidate
	}
	if !permitted {
		return "", errors.New("site is not in a state that permits this transition")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, transition, now, siteID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, kind, "site", siteID, "queued", "waiting", 0, actor.ID, kind+":"+siteID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, kind+".requested", "site", siteID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) OtherActiveSiteUsesPHP(ctx context.Context, siteID, version string) (bool, error) {
	if model.ValidateSiteID(siteID) != nil || !model.ValidPHPVersion(version) {
		return false, errors.New("invalid PHP usage query")
	}
	var count int
	// A reserved or partially failed site may still have a live pool. Only a
	// confirmed disabled site (or one that never provisioned) releases its PHP
	// branch; domain changes and deletion must not stop a neighbor's runtime.
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id<>? AND status NOT IN ('disabled','failed') AND kind IN ('wordpress','php') AND php_version=?`, siteID, version).Scan(&count)
	return count != 0, err
}
