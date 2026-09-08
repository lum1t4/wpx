package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

const BackupHistoryMigration = `ALTER TABLE jobs ADD COLUMN started_at TEXT;
CREATE INDEX jobs_site_backup_history ON jobs(target_id,created_at DESC,id DESC) WHERE kind='site.backup' AND target_type='site';`

// ListSiteBackupRuns reads job state directly. Snapshot rows are joined only to
// describe current restore availability; retention never rewrites run success.
func (s *Store) ListSiteBackupRuns(ctx context.Context, siteID string, limit int) ([]model.BackupRun, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,j.target_id,j.status,j.phase,j.progress,j.idempotency_key,
		j.payload_json,j.result_json,j.created_at,COALESCE(j.started_at,''),COALESCE(j.finished_at,''),
		j.initiator_id IS NOT NULL,COALESCE(t.name,''),
		CASE WHEN json_valid(j.payload_json) AND json_valid(j.result_json) THEN EXISTS(
			SELECT 1 FROM backup_snapshots snapshot
			WHERE snapshot.site_id=j.target_id
			AND snapshot.target_id=json_extract(j.payload_json,'$.target_id')
			AND snapshot.restic_snapshot_id=json_extract(j.result_json,'$.snapshot_id')
		) ELSE 0 END
		FROM jobs j
		LEFT JOIN backup_targets t ON t.id=CASE WHEN json_valid(j.payload_json) THEN json_extract(j.payload_json,'$.target_id') ELSE NULL END
		WHERE j.kind='site.backup' AND j.target_type='site' AND j.target_id=?
		ORDER BY j.created_at DESC,j.id DESC LIMIT ?`, siteID, limit)
	if err != nil {
		return nil, fmt.Errorf("list site backup runs: %w", err)
	}
	defer rows.Close()
	runs := make([]model.BackupRun, 0)
	now := s.now().UTC()
	for rows.Next() {
		var run model.BackupRun
		var idempotencyKey, payloadJSON, resultJSON string
		var hasInitiator bool
		if err := rows.Scan(&run.JobID, &run.SiteID, &run.Status, &run.Phase, &run.Progress, &idempotencyKey,
			&payloadJSON, &resultJSON, &run.QueuedAt, &run.StartedAt, &run.FinishedAt, &hasInitiator, &run.TargetName, &run.RestorePointAvailable); err != nil {
			return nil, fmt.Errorf("scan site backup run: %w", err)
		}
		var payload struct {
			TargetID     string `json:"target_id"`
			ScheduleID   string `json:"schedule_id"`
			ScheduledFor string `json:"scheduled_for"`
		}
		if json.Unmarshal([]byte(payloadJSON), &payload) == nil {
			run.TargetID = payload.TargetID
			run.ScheduledFor = payload.ScheduledFor
		}
		if run.TargetName == "" {
			run.TargetName = "Unavailable storage"
		}
		if strings.HasPrefix(idempotencyKey, "backup.schedule:") || payload.ScheduleID != "" {
			run.Source = "Scheduled"
		} else if strings.HasPrefix(idempotencyKey, "site.backup:") || hasInitiator {
			run.Source = "Manual"
		} else {
			run.Source = "Unknown"
		}
		if run.Status == "succeeded" {
			var result struct {
				SnapshotID         string `json:"snapshot_id"`
				RecoverySnapshotID string `json:"recovery_snapshot_id"`
			}
			if json.Unmarshal([]byte(resultJSON), &result) == nil {
				if model.ValidResticSnapshotID(result.SnapshotID) {
					run.SnapshotID = result.SnapshotID
				}
				if model.ValidResticSnapshotID(result.RecoverySnapshotID) {
					run.RecoverySnapshotID = result.RecoverySnapshotID
				}
			}
			if run.SnapshotID == "" {
				run.RestorePointAvailable = false
			}
		}
		if run.Status == "failed" {
			run.FailureSummary = "Backup failed"
		}
		run.Duration = backupRunDuration(run.StartedAt, run.FinishedAt, run.Status, now)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate site backup runs: %w", err)
	}
	return runs, nil
}

func backupRunDuration(startedAt, finishedAt, status string, now time.Time) string {
	if startedAt == "" {
		return ""
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ""
	}
	finished := now
	if finishedAt != "" {
		finished, err = time.Parse(time.RFC3339Nano, finishedAt)
		if err != nil {
			return ""
		}
	} else if status != "running" {
		return ""
	}
	duration := finished.Sub(started)
	if duration < 0 {
		return ""
	}
	if duration < time.Second {
		return "<1s"
	}
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm %ds", int(duration/time.Minute), int(duration/time.Second)%60)
	}
	return fmt.Sprintf("%dh %dm", int(duration/time.Hour), int(duration/time.Minute)%60)
}
