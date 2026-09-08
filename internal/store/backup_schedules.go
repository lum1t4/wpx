package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) SetBackupSchedule(ctx context.Context, actor User, siteID, targetID string, intervalHours int, retention model.BackupRetention, restoreTestDays int) error {
	if err := model.ValidateSiteID(siteID); err != nil {
		return err
	}
	if intervalHours != 6 && intervalHours != 24 && intervalHours != 168 {
		return errors.New("backup frequency must be every 6 hours, daily, or weekly")
	}
	if err := model.ValidateBackupRetention(retention, false); err != nil {
		return err
	}
	if restoreTestDays != 0 && restoreTestDays != 7 && restoreTestDays != 30 {
		return errors.New("restore tests may be disabled, weekly, or monthly")
	}
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	nextRun := now.Add(time.Duration(intervalHours) * time.Hour).Format(time.RFC3339Nano)
	var nextRestoreTest any
	if restoreTestDays != 0 {
		nextRestoreTest = now.Add(time.Duration(restoreTestDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteReady, targetReady int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=? AND status='active'`, siteID).Scan(&siteReady); err != nil || siteReady != 1 {
		return errors.New("backup schedule requires an active site")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'`, targetID).Scan(&targetReady); err != nil || targetReady != 1 {
		return errors.New("backup schedule requires active storage")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_schedules(id,site_id,target_id,interval_hours,next_run,enabled,created_at,updated_at,keep_daily,keep_weekly,keep_monthly,restore_test_interval_days,next_restore_test)
		VALUES(?,?,?,?,?,1,?,?,?,?,?,?,?) ON CONFLICT(site_id,target_id) DO UPDATE SET interval_hours=excluded.interval_hours,next_run=excluded.next_run,enabled=1,updated_at=excluded.updated_at,keep_daily=excluded.keep_daily,keep_weekly=excluded.keep_weekly,keep_monthly=excluded.keep_monthly,restore_test_interval_days=excluded.restore_test_interval_days,next_restore_test=excluded.next_restore_test`, mustID("sch_"), siteID, targetID, intervalHours, nextRun, nowText, nowText, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, restoreTestDays, nextRestoreTest); err != nil {
		return fmt.Errorf("save backup schedule: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "backup_schedule.saved", "site", siteID, "success", nowText); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListSiteBackupSchedules(ctx context.Context, siteID string) ([]model.BackupSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,target_id,interval_hours,next_run,enabled,keep_daily,keep_weekly,keep_monthly,restore_test_interval_days,COALESCE(next_restore_test,''),COALESCE(last_restore_test_at,''),last_restore_test_status FROM backup_schedules WHERE site_id=? ORDER BY next_run`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []model.BackupSchedule
	for rows.Next() {
		var schedule model.BackupSchedule
		if err := rows.Scan(&schedule.ID, &schedule.SiteID, &schedule.TargetID, &schedule.IntervalHours, &schedule.NextRun, &schedule.Enabled, &schedule.Retention.KeepDaily, &schedule.Retention.KeepWeekly, &schedule.Retention.KeepMonthly, &schedule.RestoreTestIntervalDays, &schedule.NextRestoreTest, &schedule.LastRestoreTestAt, &schedule.LastRestoreTestStatus); err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

// EnqueueDueBackups advances each schedule in the same transaction that
// creates its job. A restart cannot lose a due run or enqueue it twice.
func (s *Store) EnqueueDueBackups(ctx context.Context) (int, error) {
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.site_id,s.target_id,s.interval_hours,s.next_run,s.keep_daily,s.keep_weekly,s.keep_monthly FROM backup_schedules s
		JOIN sites site ON site.id=s.site_id AND site.status='active'
		JOIN backup_targets target ON target.id=s.target_id AND target.status='active'
		WHERE s.enabled=1 AND s.next_run<=? ORDER BY s.next_run LIMIT 100`, nowText)
	if err != nil {
		return 0, err
	}
	type dueSchedule struct {
		id, siteID, targetID, nextRun string
		intervalHours                 int
		retention                     model.BackupRetention
	}
	var due []dueSchedule
	for rows.Next() {
		var schedule dueSchedule
		if err := rows.Scan(&schedule.id, &schedule.siteID, &schedule.targetID, &schedule.intervalHours, &schedule.nextRun, &schedule.retention.KeepDaily, &schedule.retention.KeepWeekly, &schedule.retention.KeepMonthly); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, schedule)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, schedule := range due {
		jobID := mustID("job_")
		payloadBytes, err := json.Marshal(struct {
			TargetID     string                `json:"target_id"`
			Retention    model.BackupRetention `json:"retention"`
			ScheduleID   string                `json:"schedule_id"`
			ScheduledFor string                `json:"scheduled_for"`
		}{TargetID: schedule.targetID, Retention: schedule.retention, ScheduleID: schedule.id, ScheduledFor: schedule.nextRun})
		if err != nil {
			return 0, err
		}
		payload := string(payloadBytes)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO jobs(id,kind,target_type,target_id,status,phase,progress,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.backup", "site", schedule.siteID, "queued", "waiting", 0, "backup.schedule:"+schedule.id+":"+schedule.nextRun, payload, nowText, nowText); err != nil {
			return 0, err
		}
		nextRun := now.Add(time.Duration(schedule.intervalHours) * time.Hour).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET next_run=?,updated_at=? WHERE id=? AND next_run=?`, nextRun, nowText, schedule.id, schedule.nextRun); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(due), nil
}
