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
)

// CreateStaging records the clone as an ordinary managed site linked to its
// production parent. It copies explicit grants so collaborators keep the same
// access without silently gaining access to unrelated sites.
func (s *Store) CreateStaging(ctx context.Context, actor User, sourceID, stagingID, stagingDomain string) (string, string, error) {
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", "", err
	}
	password, err := randomID("", 18)
	if err != nil {
		return "", "", err
	}
	ciphertext, err := s.encrypt([]byte(password))
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
	err = tx.QueryRowContext(ctx, `SELECT id,domain,kind,php_version,php_eol_ack,status,environment,redis_enabled,fastcgi_cache_enabled,wordpress_multisite FROM sites WHERE id=?`, sourceID).
		Scan(&source.ID, &source.Domain, &source.Kind, &source.PHPVersion, &source.AllowEOL, &source.Status, &source.Environment, &source.RedisEnabled, &source.FastCGICacheEnabled, &source.WordPressMultisite)
	if err != nil || source.Kind != model.WordPress || source.Status != "active" || source.Environment != "production" {
		return "", "", errors.New("staging requires an active production WordPress site")
	}
	staging := model.Site{ID: stagingID, Domain: stagingDomain, Kind: model.WordPress, PHPVersion: source.PHPVersion, AllowEOL: source.AllowEOL, Status: "queued", Environment: "staging", ParentSiteID: source.ID, RedisEnabled: source.RedisEnabled, FastCGICacheEnabled: source.FastCGICacheEnabled, WordPressMultisite: source.WordPressMultisite}
	if err := model.ValidateSite(staging); err != nil {
		return "", "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sites(id,domain,kind,php_version,upstream,php_eol_ack,status,environment,parent_site_id,redis_enabled,fastcgi_cache_enabled,wordpress_multisite,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, staging.ID, staging.Domain, staging.Kind, staging.PHPVersion, "", staging.AllowEOL, "queued", staging.Environment, staging.ParentSiteID, staging.RedisEnabled, staging.FastCGICacheEnabled, staging.WordPressMultisite, now, now)
	if err != nil {
		return "", "", fmt.Errorf("insert staging site: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO staging_credentials(site_id,username,password_ciphertext) VALUES(?,?,?)`, staging.ID, "wpx", ciphertext); err != nil {
		return "", "", fmt.Errorf("protect staging credentials: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_grants(user_id,site_id,capability)
		SELECT user_id,?,capability FROM site_grants WHERE site_id=?`, staging.ID, source.ID); err != nil {
		return "", "", fmt.Errorf("copy staging access: %w", err)
	}
	if err := copyStagingDNSRecords(ctx, tx, actor, source, staging, now); err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(map[string]string{"source_id": source.ID})
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.staging_create", "site", staging.ID, "queued", "waiting", 0, actor.ID, "wordpress.staging_create:"+staging.ID, string(payload), now, now); err != nil {
		return "", "", fmt.Errorf("enqueue staging creation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "staging.requested", "site", staging.ID, "success", string(payload), now); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return jobID, password, nil
}

func copyStagingDNSRecords(ctx context.Context, tx *sql.Tx, actor User, source, staging model.Site, now string) error {
	names := []string{source.Domain}
	if source.WordPressMultisite == model.MultisiteSubdomains {
		names = append(names, "*."+source.Domain)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	arguments := []any{source.ID}
	for _, name := range names {
		arguments = append(arguments, name)
	}
	rows, err := tx.QueryContext(ctx, `SELECT provider_id,name,type,value,ttl,proxied FROM dns_records WHERE site_id=? AND name IN (`+placeholders+`) AND status='active' AND type IN ('A','AAAA','CNAME')`, arguments...)
	if err != nil {
		return err
	}
	type sourceRecord struct {
		providerID, name, recordType, value string
		ttl                                 int
		proxied                             bool
	}
	var records []sourceRecord
	for rows.Next() {
		var record sourceRecord
		if err := rows.Scan(&record.providerID, &record.name, &record.recordType, &record.value, &record.ttl, &record.proxied); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, sourceRecord := range records {
		recordID, jobID := mustID("rec_"), mustID("job_")
		targetName := staging.Domain
		if sourceRecord.name == "*."+source.Domain {
			targetName = "*." + staging.Domain
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO dns_records(id,site_id,provider_id,name,type,value,ttl,proxied,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, recordID, staging.ID, sourceRecord.providerID, targetName, sourceRecord.recordType, sourceRecord.value, sourceRecord.ttl, sourceRecord.proxied, "queued", now, now); err != nil {
			return fmt.Errorf("copy staging DNS record: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "dns.record_apply", "dns_record", recordID, "queued", "waiting", 0, actor.ID, "dns.record_apply:"+recordID, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) StagingCredential(ctx context.Context, siteID string) (string, string, error) {
	var username string
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, `SELECT username,password_ciphertext FROM staging_credentials WHERE site_id=?`, siteID).Scan(&username, &ciphertext); err != nil {
		return "", "", err
	}
	password, err := s.decrypt(ciphertext)
	if err != nil {
		return "", "", err
	}
	return username, string(password), nil
}

func (s *Store) EnqueueStagingSync(ctx context.Context, actor User, stagingID string) (string, error) {
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
	var sourceID, stagingStatus, sourceStatus, environment string
	err = tx.QueryRowContext(ctx, `SELECT s.parent_site_id,s.status,p.status,s.environment FROM sites s
		JOIN sites p ON p.id=s.parent_site_id WHERE s.id=? AND s.kind='wordpress'`, stagingID).
		Scan(&sourceID, &stagingStatus, &sourceStatus, &environment)
	if err != nil || environment != "staging" || stagingStatus != "active" || sourceStatus != "active" {
		return "", errors.New("sync requires active production and staging sites")
	}
	payload, err := json.Marshal(map[string]string{"source_id": sourceID})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.staging_sync", "site", stagingID, "queued", "waiting", 0, actor.ID, "wordpress.staging_sync:"+stagingID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "staging.sync_requested", "site", stagingID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) EnqueueStagingDeploy(ctx context.Context, actor User, stagingID, backupTargetID string, selection model.StagingSelection) (string, error) {
	if err := model.ValidateStagingSelection(selection); err != nil {
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
	var productionID, stagingStatus, productionStatus, environment string
	err = tx.QueryRowContext(ctx, `SELECT s.parent_site_id,s.status,p.status,s.environment FROM sites s
		JOIN sites p ON p.id=s.parent_site_id WHERE s.id=? AND s.kind='wordpress'`, stagingID).
		Scan(&productionID, &stagingStatus, &productionStatus, &environment)
	if err != nil || environment != "staging" || stagingStatus != "active" || productionStatus != "active" {
		return "", errors.New("deploy requires active production and staging sites")
	}
	var targetReady int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM backup_targets WHERE id=? AND status='active'", backupTargetID).Scan(&targetReady); err != nil || targetReady != 1 {
		return "", errors.New("an active backup target is required for production recovery")
	}
	payload, err := json.Marshal(struct {
		StagingID string                 `json:"staging_id"`
		TargetID  string                 `json:"target_id"`
		Selection model.StagingSelection `json:"selection"`
	}{StagingID: stagingID, TargetID: backupTargetID, Selection: selection})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, jobID, "wordpress.staging_deploy", "site", productionID, "queued", "waiting", 0, actor.ID, "wordpress.staging_deploy:"+productionID+":"+jobID, string(payload), now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "staging.deploy_requested", "site", productionID, "success", string(payload), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}
