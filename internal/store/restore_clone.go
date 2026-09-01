package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

// EnqueueRestoreClone creates the destination and durable job atomically. A
// staging password is returned once; only its encrypted form remains in state.
func (s *Store) EnqueueRestoreClone(ctx context.Context, actor User, sourceID, snapshotRecordID, targetID, targetDomain, destination string) (string, string, error) {
	if destination != "production" && destination != "staging" {
		return "", "", errors.New("restore destination must be a new site or staging")
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var source model.Site
	err = tx.QueryRowContext(ctx, `SELECT id,domain,kind,php_version,upstream,php_eol_ack,status,tls_status,environment,COALESCE(parent_site_id,''),redis_enabled,fastcgi_cache_enabled,wordpress_multisite FROM sites WHERE id=?`, sourceID).
		Scan(&source.ID, &source.Domain, &source.Kind, &source.PHPVersion, &source.Upstream, &source.AllowEOL, &source.Status, &source.TLSStatus, &source.Environment, &source.ParentSiteID, &source.RedisEnabled, &source.FastCGICacheEnabled, &source.WordPressMultisite)
	if err != nil || source.Status != "active" {
		return "", "", errors.New("restore source must be an active site")
	}
	var backupTargetID, snapshotID string
	if err := tx.QueryRowContext(ctx, `SELECT target_id,restic_snapshot_id FROM backup_snapshots WHERE id=? AND site_id=?`, snapshotRecordID, source.ID).Scan(&backupTargetID, &snapshotID); err != nil {
		return "", "", errors.New("restore point is unavailable for this site")
	}
	if destination == "staging" && source.Kind != model.WordPress {
		return "", "", errors.New("protected staging restore is available only for WordPress")
	}
	parentID := ""
	if destination == "staging" {
		parentID = source.ID
		if source.Environment == "staging" && source.ParentSiteID != "" {
			parentID = source.ParentSiteID
		}
	}
	target := model.Site{
		ID: targetID, Domain: targetDomain, Kind: source.Kind, PHPVersion: source.PHPVersion,
		Upstream: source.Upstream, AllowEOL: source.AllowEOL, Status: "queued",
		Environment: destination, ParentSiteID: parentID,
		RedisEnabled: source.RedisEnabled, FastCGICacheEnabled: source.FastCGICacheEnabled, WordPressMultisite: source.WordPressMultisite,
	}
	if err := model.ValidateSite(target); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sites(id,domain,kind,php_version,upstream,php_eol_ack,status,environment,parent_site_id,redis_enabled,fastcgi_cache_enabled,wordpress_multisite,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, target.ID, target.Domain, target.Kind, target.PHPVersion, target.Upstream, target.AllowEOL, target.Status, target.Environment, nullableSiteID(target.ParentSiteID), target.RedisEnabled, target.FastCGICacheEnabled, target.WordPressMultisite, now, now); err != nil {
		return "", "", fmt.Errorf("create restore destination: %w", err)
	}
	password := ""
	if destination == "staging" {
		password, err = randomID("", 18)
		if err != nil {
			return "", "", err
		}
		ciphertext, err := s.encrypt([]byte(password))
		if err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO staging_credentials(site_id,username,password_ciphertext) VALUES(?,?,?)`, target.ID, "wpx", ciphertext); err != nil {
			return "", "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_grants(user_id,site_id,capability) SELECT user_id,?,capability FROM site_grants WHERE site_id=?`, target.ID, source.ID); err != nil {
		return "", "", err
	}
	if destination == "staging" {
		if err := copyStagingDNSRecords(ctx, tx, actor, source, target, now); err != nil {
			return "", "", err
		}
	}
	payload, err := json.Marshal(struct {
		SourceID       string `json:"source_id"`
		BackupTargetID string `json:"target_id"`
		SnapshotID     string `json:"snapshot_id"`
	}{SourceID: source.ID, BackupTargetID: backupTargetID, SnapshotID: snapshotID})
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.restore_clone", "site", target.ID, "queued", "waiting", 0, actor.ID, "site.restore_clone:"+target.ID, string(payload), now, now); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "restore_clone.requested", "site", target.ID, "success", string(payload), now); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return jobID, password, nil
}

func nullableSiteID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
