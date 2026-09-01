package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) EnqueueWordPressUpdate(ctx context.Context, actor User, siteID, backupTargetID string, update model.WordPressUpdate) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	if err := model.ValidateWordPressUpdate(update); err != nil {
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
		return "", errors.New("update requires an active WordPress site")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'`, backupTargetID).Scan(&ready); err != nil || ready != 1 {
		return "", errors.New("an active backup target is required for update recovery")
	}
	payload, err := json.Marshal(struct {
		TargetID string                `json:"target_id"`
		Update   model.WordPressUpdate `json:"update"`
	}{TargetID: backupTargetID, Update: update})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.update", "site", siteID, "queued", "waiting", 0, actor.ID, "wordpress.update:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	detail, _ := json.Marshal(map[string]string{"component": string(update.Component), "name": update.Name, "backup_target_id": backupTargetID})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "wordpress.update_requested", "site", siteID, "success", string(detail), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}
