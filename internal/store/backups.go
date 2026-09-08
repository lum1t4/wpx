package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (s *Store) CreateS3Target(ctx context.Context, actor User, target model.BackupTarget) (model.BackupTarget, string, error) {
	target.Kind = model.BackupS3
	target.Name = strings.TrimSpace(target.Name)
	target.Endpoint = strings.TrimRight(strings.TrimSpace(target.Endpoint), "/")
	target.Bucket = strings.TrimSpace(target.Bucket)
	target.Prefix = strings.Trim(strings.TrimSpace(target.Prefix), "/")
	target.Region = strings.TrimSpace(target.Region)
	if target.Region == "" {
		target.Region = "us-east-1"
	}
	if target.BucketLookup == "" {
		target.BucketLookup = "auto"
	}
	generatedPassword := ""
	if target.RepositoryPassword == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return model.BackupTarget{}, "", err
		}
		generatedPassword = base64.RawURLEncoding.EncodeToString(secret)
		target.RepositoryPassword = generatedPassword
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return model.BackupTarget{}, "", err
	}
	return s.createBackupTarget(ctx, actor, target, generatedPassword)
}

func (s *Store) CreateGoogleDriveTarget(ctx context.Context, actor User, target model.BackupTarget) (model.BackupTarget, string, error) {
	target.Kind = model.BackupGoogleDrive
	target.Name = strings.TrimSpace(target.Name)
	target.DriveFolder = strings.Trim(strings.TrimSpace(target.DriveFolder), "/")
	target.GoogleClientID = strings.TrimSpace(target.GoogleClientID)
	target.GoogleClientSecret = strings.TrimSpace(target.GoogleClientSecret)
	target.GoogleToken = strings.TrimSpace(target.GoogleToken)
	target.GoogleSharedDrive = strings.TrimSpace(target.GoogleSharedDrive)
	generatedPassword := ""
	if target.RepositoryPassword == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return model.BackupTarget{}, "", err
		}
		generatedPassword = base64.RawURLEncoding.EncodeToString(secret)
		target.RepositoryPassword = generatedPassword
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return model.BackupTarget{}, "", err
	}
	return s.createBackupTarget(ctx, actor, target, generatedPassword)
}

func (s *Store) createBackupTarget(ctx context.Context, actor User, target model.BackupTarget, generatedPassword string) (model.BackupTarget, string, error) {
	id, err := randomID("bkt_", 12)
	if err != nil {
		return model.BackupTarget{}, "", err
	}
	target.ID, target.Status = id, "queued"
	encoded, err := json.Marshal(target)
	if err != nil {
		return model.BackupTarget{}, "", err
	}
	ciphertext, err := s.encrypt(encoded)
	if err != nil {
		return model.BackupTarget{}, "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return model.BackupTarget{}, "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.BackupTarget{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_targets(id,name,kind,status,config_ciphertext,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, target.Name, target.Kind, target.Status, ciphertext, now, now); err != nil {
		return model.BackupTarget{}, "", fmt.Errorf("insert backup target: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "backup.target_init", "backup_target", id, "queued", "waiting", 0, actor.ID, "backup.target_init:"+id, now, now); err != nil {
		return model.BackupTarget{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "backup_target.created", "backup_target", id, "success", now); err != nil {
		return model.BackupTarget{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return model.BackupTarget{}, "", err
	}
	return publicBackupTarget(target), generatedPassword, nil
}

func (s *Store) BackupTarget(ctx context.Context, id string) (model.BackupTarget, error) {
	var ciphertext []byte
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT config_ciphertext,status FROM backup_targets WHERE id=?`, id).Scan(&ciphertext, &status); err != nil {
		return model.BackupTarget{}, err
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		return model.BackupTarget{}, err
	}
	var target model.BackupTarget
	if err := json.Unmarshal(plaintext, &target); err != nil {
		return model.BackupTarget{}, err
	}
	target.Status = status
	if err := model.ValidateBackupTarget(target); err != nil {
		return model.BackupTarget{}, fmt.Errorf("stored backup target is invalid: %w", err)
	}
	return target, nil
}

func (s *Store) ListBackupTargets(ctx context.Context) ([]model.BackupTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,status FROM backup_targets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []model.BackupTarget
	for rows.Next() {
		var target model.BackupTarget
		if err := rows.Scan(&target.ID, &target.Name, &target.Kind, &target.Status); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func publicBackupTarget(target model.BackupTarget) model.BackupTarget {
	target.AccessKey = ""
	target.SecretKey = ""
	target.RepositoryPassword = ""
	target.GoogleClientID = ""
	target.GoogleClientSecret = ""
	target.GoogleToken = ""
	return target
}

func (s *Store) EnqueueSiteBackup(ctx context.Context, actor User, siteID, targetID string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]string{"target_id": targetID})
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var siteReady, targetReady int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sites WHERE id=? AND status='active'", siteID).Scan(&siteReady); err != nil || siteReady != 1 {
		return "", errors.New("backup requires an active site")
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'", targetID).Scan(&targetReady); err != nil || targetReady != 1 {
		return "", errors.New("backup target is not active")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.backup", "site", siteID, "queued", "waiting", 0, actor.ID, "site.backup:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "backup.requested", "site", siteID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

// RetryBackupJob returns a backup with an uncertain broker outcome to the queue.
// The existing job and idempotency key are retained so the provisioner can
// reconcile a completed remote snapshot instead of creating another one.
func (s *Store) RetryBackupJob(ctx context.Context, jobID, detail string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind='site.backup' AND status='running'`, detail, s.now().UTC().Format(time.RFC3339Nano), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("backup job is not awaiting broker confirmation")
	}
	return nil
}

func (s *Store) ListSiteSnapshots(ctx context.Context, siteID string) ([]model.BackupSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,target_id,restic_snapshot_id,created_at FROM backup_snapshots WHERE site_id=? ORDER BY created_at DESC`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []model.BackupSnapshot
	for rows.Next() {
		var snapshot model.BackupSnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.SiteID, &snapshot.TargetID, &snapshot.ResticSnapshotID, &snapshot.CreatedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

func (s *Store) EnqueueSiteRestore(ctx context.Context, actor User, siteID, snapshotRecordID string) (string, error) {
	if err := model.ValidateSiteID(siteID); err != nil {
		return "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var siteReady int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sites WHERE id=? AND status='active'", siteID).Scan(&siteReady); err != nil || siteReady != 1 {
		return "", errors.New("restore requires an active site")
	}
	var targetID, snapshotID, targetStatus string
	err = tx.QueryRowContext(ctx, `SELECT s.target_id,s.restic_snapshot_id,t.status FROM backup_snapshots s
		JOIN backup_targets t ON t.id=s.target_id WHERE s.id=? AND s.site_id=?`, snapshotRecordID, siteID).Scan(&targetID, &snapshotID, &targetStatus)
	if err != nil || targetStatus != "active" || !model.ValidResticSnapshotID(snapshotID) {
		return "", errors.New("restore point is unavailable")
	}
	payload, err := json.Marshal(map[string]string{"target_id": targetID, "snapshot_id": snapshotID})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.restore", "site", siteID, "queued", "waiting", 0, actor.ID, "site.restore:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "restore.requested", "site", siteID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}
