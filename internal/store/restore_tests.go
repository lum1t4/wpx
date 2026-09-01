package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) EnqueueRestoreTest(ctx context.Context, actor User, siteID, snapshotRecordID string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	var targetID, snapshotID string
	if err := s.db.QueryRowContext(ctx, `SELECT target_id,restic_snapshot_id FROM backup_snapshots WHERE id=? AND site_id=?`, snapshotRecordID, siteID).Scan(&targetID, &snapshotID); err != nil {
		return "", errors.New("restore point is unavailable for this site")
	}
	jobID := mustID("job_")
	payload, _ := json.Marshal(map[string]string{"target_id": targetID, "snapshot_id": snapshotID})
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.restore_test", "site", siteID, "queued", "waiting", 0, actor.ID, "site.restore_test:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) SetRestoreTestSchedule(ctx context.Context, actor User, siteID, targetID string, intervalDays int) error {
	if err := model.ValidateSiteID(siteID); err != nil {
		return err
	}
	if intervalDays != 0 && intervalDays != 7 && intervalDays != 30 {
		return errors.New("restore tests may be disabled, weekly, or monthly")
	}
	now := s.now().UTC()
	var next any
	if intervalDays != 0 {
		next = now.Add(time.Duration(intervalDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	nowText := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET restore_test_interval_days=?,next_restore_test=?,updated_at=? WHERE site_id=? AND target_id=?`, intervalDays, next, nowText, siteID, targetID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("create a backup schedule for this storage target first")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "restore_test_schedule.saved", "site", siteID, "success", nowText); err != nil {
		return err
	}
	return tx.Commit()
}

// EnqueueDueRestoreTests binds each due run to the newest retained snapshot in
// the same transaction that advances its schedule. Retention reconciliation
// therefore cannot leave a scheduled job pointing at a locally obsolete ID.
func (s *Store) EnqueueDueRestoreTests(ctx context.Context) (int, error) {
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.site_id,s.target_id,s.restore_test_interval_days,s.next_restore_test,b.id,b.restic_snapshot_id
		FROM backup_schedules s
		JOIN sites site ON site.id=s.site_id AND site.status='active'
		JOIN backup_targets target ON target.id=s.target_id AND target.status='active'
		JOIN backup_snapshots b ON b.id=(SELECT newest.id FROM backup_snapshots newest WHERE newest.site_id=s.site_id AND newest.target_id=s.target_id ORDER BY newest.created_at DESC,newest.id DESC LIMIT 1)
		WHERE s.enabled=1 AND s.restore_test_interval_days>0 AND s.next_restore_test<=? ORDER BY s.next_restore_test LIMIT 50`, nowText)
	if err != nil {
		return 0, err
	}
	type dueTest struct {
		scheduleID, siteID, targetID, nextRun, snapshotRecordID, snapshotID string
		intervalDays                                                        int
	}
	var due []dueTest
	for rows.Next() {
		var test dueTest
		if err := rows.Scan(&test.scheduleID, &test.siteID, &test.targetID, &test.intervalDays, &test.nextRun, &test.snapshotRecordID, &test.snapshotID); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, test)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, test := range due {
		jobID := mustID("job_")
		payload, _ := json.Marshal(map[string]string{"target_id": test.targetID, "snapshot_id": test.snapshotID, "schedule_id": test.scheduleID})
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,kind,target_type,target_id,status,phase,progress,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.restore_test", "site", test.siteID, "queued", "waiting", 0, "backup.restore_test:"+test.scheduleID+":"+test.nextRun, string(payload), nowText, nowText); err != nil {
			return 0, err
		}
		nextRun := now.Add(time.Duration(test.intervalDays) * 24 * time.Hour).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET next_restore_test=?,updated_at=? WHERE id=? AND next_restore_test=?`, nextRun, nowText, test.scheduleID, test.nextRun); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(due), nil
}
