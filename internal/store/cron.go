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

const CronMigration = `CREATE TABLE cron_schedules (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	expression TEXT NOT NULL,
	command_json TEXT NOT NULL,
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	apply_status TEXT NOT NULL CHECK(apply_status IN ('pending','applied','failed','pending_delete')),
	apply_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX cron_schedules_site ON cron_schedules(site_id,name);
CREATE TABLE wordpress_cron_settings (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	replaced INTEGER NOT NULL CHECK(replaced IN (0,1)),
	expression TEXT NOT NULL,
	apply_status TEXT NOT NULL CHECK(apply_status IN ('pending','applied','failed')),
	apply_error TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);`

func (s *Store) ListCronSchedules(ctx context.Context, siteID string) ([]model.CronSchedule, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,name,expression,command_json,enabled,apply_status,apply_error,created_at,updated_at FROM cron_schedules WHERE site_id=? AND apply_status!='pending_delete' ORDER BY name,id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.CronSchedule
	for rows.Next() {
		schedule, err := scanCronSchedule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, schedule)
	}
	return result, rows.Err()
}

type cronScanner interface{ Scan(...any) error }

type CronJobPayload struct {
	Action   string                      `json:"action"`
	Schedule *model.CronSchedule         `json:"schedule,omitempty"`
	Setting  *model.WordPressCronSetting `json:"setting,omitempty"`
}

func enqueueCronJob(ctx context.Context, tx *sql.Tx, actor User, siteID, kind string, payload CronJobPayload, now string) error {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE target_type='site' AND target_id=? AND kind IN ('site.cron_apply','site.wordpress_cron_apply') AND status IN ('queued','running')`, siteID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("wait for the current cron change to finish")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	jobID := mustID("job_")
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, kind, "site", siteID, "queued", "waiting", 0, actor.ID, kind+":"+jobID, string(encoded), now, now)
	return err
}

func scanCronSchedule(row cronScanner) (model.CronSchedule, error) {
	var schedule model.CronSchedule
	var commandJSON string
	if err := row.Scan(&schedule.ID, &schedule.SiteID, &schedule.Name, &schedule.Expression, &commandJSON, &schedule.Enabled, &schedule.ApplyStatus, &schedule.ApplyError, &schedule.CreatedAt, &schedule.UpdatedAt); err != nil {
		return schedule, err
	}
	if err := json.Unmarshal([]byte(commandJSON), &schedule.Command); err != nil || model.ValidateCronCommand(schedule.Command) != nil {
		return model.CronSchedule{}, errors.New("stored cron command is invalid")
	}
	return schedule, nil
}

func (s *Store) CronSchedule(ctx context.Context, siteID, id string) (model.CronSchedule, error) {
	return scanCronSchedule(s.db.QueryRowContext(ctx, `SELECT id,site_id,name,expression,command_json,enabled,apply_status,apply_error,created_at,updated_at FROM cron_schedules WHERE site_id=? AND id=?`, siteID, id))
}

func (s *Store) CreateCronSchedule(ctx context.Context, actor User, schedule model.CronSchedule) (model.CronSchedule, error) {
	if !s.UserCanSite(ctx, actor, schedule.SiteID, rbac.ManageAllSites) {
		return schedule, errors.New("permission denied")
	}
	schedule.ID = mustID("cron_")
	schedule.Name = strings.TrimSpace(schedule.Name)
	schedule.Expression = strings.TrimSpace(schedule.Expression)
	schedule.ApplyStatus, schedule.ApplyError = "pending", ""
	if err := model.ValidateCronSchedule(schedule); err != nil {
		return schedule, err
	}
	command, _ := json.Marshal(schedule.Command)
	now := s.now().UTC().Format(time.RFC3339Nano)
	schedule.CreatedAt, schedule.UpdatedAt = now, now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return schedule, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO cron_schedules(id,site_id,name,expression,command_json,enabled,apply_status,apply_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, schedule.ID, schedule.SiteID, schedule.Name, schedule.Expression, string(command), schedule.Enabled, schedule.ApplyStatus, "", now, now)
	if err != nil {
		return schedule, fmt.Errorf("save cron schedule: %w", err)
	}
	if err := enqueueCronJob(ctx, tx, actor, schedule.SiteID, "site.cron_apply", CronJobPayload{Action: "apply", Schedule: &schedule}, now); err != nil {
		return schedule, err
	}
	if err := tx.Commit(); err != nil {
		return schedule, err
	}
	return schedule, nil
}

func (s *Store) UpdateCronSchedule(ctx context.Context, actor User, schedule model.CronSchedule) error {
	if !s.UserCanSite(ctx, actor, schedule.SiteID, rbac.ManageAllSites) {
		return errors.New("permission denied")
	}
	schedule.Name, schedule.Expression = strings.TrimSpace(schedule.Name), strings.TrimSpace(schedule.Expression)
	schedule.ApplyStatus, schedule.ApplyError = "pending", ""
	if err := model.ValidateCronSchedule(schedule); err != nil {
		return err
	}
	command, _ := json.Marshal(schedule.Command)
	now := s.now().UTC().Format(time.RFC3339Nano)
	schedule.UpdatedAt = now
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enqueueCronJob(ctx, tx, actor, schedule.SiteID, "site.cron_apply", CronJobPayload{Action: "apply", Schedule: &schedule}, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET name=?,expression=?,command_json=?,enabled=?,apply_status='pending',apply_error='',updated_at=? WHERE id=? AND site_id=?`, schedule.Name, schedule.Expression, string(command), schedule.Enabled, now, schedule.ID, schedule.SiteID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) RequestCronScheduleDelete(ctx context.Context, actor User, siteID, id string) (model.CronSchedule, error) {
	if !s.UserCanSite(ctx, actor, siteID, rbac.ManageAllSites) {
		return model.CronSchedule{}, errors.New("permission denied")
	}
	schedule, err := s.CronSchedule(ctx, siteID, id)
	if err != nil {
		return schedule, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return schedule, err
	}
	defer tx.Rollback()
	if err := enqueueCronJob(ctx, tx, actor, siteID, "site.cron_apply", CronJobPayload{Action: "delete", Schedule: &schedule}, now); err != nil {
		return schedule, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE cron_schedules SET apply_status='pending_delete',apply_error='',updated_at=? WHERE id=? AND site_id=?`, now, id, siteID)
	if err != nil {
		return schedule, err
	}
	return schedule, tx.Commit()
}

func (s *Store) MarkCronApplyResult(ctx context.Context, siteID, id string, applyErr error) error {
	status, message := "applied", ""
	if applyErr != nil {
		status, message = "failed", applyErr.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE cron_schedules SET apply_status=?,apply_error=?,updated_at=? WHERE id=? AND site_id=?`, status, message, s.now().UTC().Format(time.RFC3339Nano), id, siteID)
	return err
}

func (s *Store) CompleteCronScheduleDelete(ctx context.Context, siteID, id string, applyErr error) error {
	if applyErr == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM cron_schedules WHERE id=? AND site_id=? AND apply_status='pending_delete'`, id, siteID)
		return err
	}
	message := applyErr.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE cron_schedules SET apply_status='failed',apply_error=?,updated_at=? WHERE id=? AND site_id=?`, message, s.now().UTC().Format(time.RFC3339Nano), id, siteID)
	return err
}

func (s *Store) WordPressCronSetting(ctx context.Context, siteID string) (model.WordPressCronSetting, error) {
	setting := model.WordPressCronSetting{SiteID: siteID, Expression: "*/5 * * * *", ApplyStatus: "applied"}
	err := s.db.QueryRowContext(ctx, `SELECT replaced,expression,apply_status,apply_error FROM wordpress_cron_settings WHERE site_id=?`, siteID).Scan(&setting.Replaced, &setting.Expression, &setting.ApplyStatus, &setting.ApplyError)
	if errors.Is(err, sql.ErrNoRows) {
		return setting, nil
	}
	return setting, err
}

func (s *Store) SetWordPressCronSetting(ctx context.Context, actor User, setting model.WordPressCronSetting) error {
	if !s.UserCanSite(ctx, actor, setting.SiteID, rbac.ManageAllSites) {
		return errors.New("permission denied")
	}
	if err := model.ValidateWordPressCronSetting(setting); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := enqueueCronJob(ctx, tx, actor, setting.SiteID, "site.wordpress_cron_apply", CronJobPayload{Action: "apply", Setting: &setting}, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO wordpress_cron_settings(site_id,replaced,expression,apply_status,apply_error,updated_at) VALUES(?,?,?,'pending','',?) ON CONFLICT(site_id) DO UPDATE SET replaced=excluded.replaced,expression=excluded.expression,apply_status='pending',apply_error='',updated_at=excluded.updated_at`, setting.SiteID, setting.Replaced, setting.Expression, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func FinishCronJobTx(ctx context.Context, tx *sql.Tx, job Job, now string, operationErr error) error {
	var payload CronJobPayload
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return err
	}
	message := ""
	status := "applied"
	if operationErr != nil {
		status = "failed"
		message = operationErr.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	if job.Kind == "site.wordpress_cron_apply" {
		_, err := tx.ExecContext(ctx, `UPDATE wordpress_cron_settings SET apply_status=?,apply_error=?,updated_at=? WHERE site_id=?`, status, message, now, job.TargetID)
		return err
	}
	if payload.Schedule == nil {
		return errors.New("cron job payload is invalid")
	}
	if payload.Action == "delete" && operationErr == nil {
		_, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules WHERE id=? AND site_id=?`, payload.Schedule.ID, job.TargetID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE cron_schedules SET apply_status=?,apply_error=?,updated_at=? WHERE id=? AND site_id=?`, status, message, now, payload.Schedule.ID, job.TargetID)
	return err
}

func (s *Store) RetryCronJob(ctx context.Context, jobID, detail string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind IN ('site.cron_apply','site.wordpress_cron_apply') AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("cron job is not awaiting broker confirmation")
	}
	return nil
}

func (s *Store) PendingCronJob(ctx context.Context, siteID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM jobs WHERE target_type='site' AND target_id=? AND kind IN ('site.cron_apply','site.wordpress_cron_apply') ORDER BY created_at DESC LIMIT 1`, siteID).Scan(&id)
	return id, err
}

func (s *Store) MarkWordPressCronApplyResult(ctx context.Context, siteID string, applyErr error) error {
	status, message := "applied", ""
	if applyErr != nil {
		status, message = "failed", applyErr.Error()
		if len(message) > 500 {
			message = message[:500]
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE wordpress_cron_settings SET apply_status=?,apply_error=?,updated_at=? WHERE site_id=?`, status, message, s.now().UTC().Format(time.RFC3339Nano), siteID)
	return err
}
