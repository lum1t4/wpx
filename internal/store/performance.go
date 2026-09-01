package store

import (
	"context"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) SetWordPressPerformance(ctx context.Context, actor User, siteID string, redisEnabled, fastCGIEnabled bool) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND kind='wordpress' AND status='active'`, siteID).Scan(&ready); err != nil || ready != 1 {
		return "", errors.New("performance settings require an active WordPress site")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET redis_enabled=?,fastcgi_cache_enabled=?,updated_at=? WHERE id=?`, redisEnabled, fastCGIEnabled, now, siteID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.performance_apply", "site", siteID, "queued", "waiting", 0, actor.ID, "wordpress.performance_apply:"+siteID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "wordpress.performance_requested", "site", siteID, "success", `{"redis":`+boolJSON(redisEnabled)+`,"fastcgi_cache":`+boolJSON(fastCGIEnabled)+`}`, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
