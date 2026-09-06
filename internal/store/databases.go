package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func (s *Store) CreateDatabase(ctx context.Context, actor User, siteID, label string) (model.Database, string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageServer) {
		return model.Database{}, "", errors.New("permission denied")
	}
	if err := model.ValidateSiteID(siteID); err != nil {
		return model.Database{}, "", err
	}
	id, err := model.NewDatabaseID()
	if err != nil {
		return model.Database{}, "", err
	}
	name, username, err := model.DatabaseIdentifiers(id)
	if err != nil {
		return model.Database{}, "", err
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return model.Database{}, "", err
	}
	password := base64.RawURLEncoding.EncodeToString(secret)
	database := model.Database{ID: id, SiteID: siteID, Label: strings.TrimSpace(label), Name: name, Username: username, Password: password, Status: "queued"}
	if err := model.ValidateDatabase(database); err != nil {
		return model.Database{}, "", err
	}
	encoded, err := json.Marshal(database)
	if err != nil {
		return model.Database{}, "", err
	}
	ciphertext, err := s.encrypt(encoded)
	if err != nil {
		return model.Database{}, "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return model.Database{}, "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Database{}, "", err
	}
	defer tx.Rollback()
	var siteDomain, siteStatus string
	if err := tx.QueryRowContext(ctx, `SELECT domain,status FROM sites WHERE id=?`, siteID).Scan(&siteDomain, &siteStatus); err != nil || siteStatus != "active" {
		return model.Database{}, "", errors.New("choose an active site")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO databases(id,site_id,label,name,username,status,config_ciphertext,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, database.ID, database.SiteID, database.Label, database.Name, database.Username, database.Status, ciphertext, now, now); err != nil {
		return model.Database{}, "", fmt.Errorf("insert database: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "database.create", "database", id, "queued", "waiting", 0, actor.ID, "database.create:"+id, now, now); err != nil {
		return model.Database{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "database.created", "database", id, "success", now); err != nil {
		return model.Database{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return model.Database{}, "", err
	}
	database.Password, database.SiteDomain = "", siteDomain
	return database, jobID, nil
}

func (s *Store) ListDatabases(ctx context.Context) ([]model.Database, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,COALESCE(d.site_id,''),COALESCE(s.domain,''),d.label,d.name,d.username,d.status,d.created_at FROM databases d LEFT JOIN sites s ON s.id=d.site_id ORDER BY d.label,d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Database
	for rows.Next() {
		var database model.Database
		if err := rows.Scan(&database.ID, &database.SiteID, &database.SiteDomain, &database.Label, &database.Name, &database.Username, &database.Status, &database.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, database)
	}
	return result, rows.Err()
}

func (s *Store) Database(ctx context.Context, id string) (model.Database, error) {
	if _, _, err := model.DatabaseIdentifiers(id); err != nil {
		return model.Database{}, err
	}
	var database model.Database
	var ciphertext []byte
	err := s.db.QueryRowContext(ctx, `SELECT d.id,COALESCE(d.site_id,''),COALESCE(s.domain,''),d.label,d.name,d.username,d.status,d.created_at,d.config_ciphertext FROM databases d LEFT JOIN sites s ON s.id=d.site_id WHERE d.id=?`, id).Scan(
		&database.ID, &database.SiteID, &database.SiteDomain, &database.Label, &database.Name, &database.Username, &database.Status, &database.CreatedAt, &ciphertext)
	if err != nil {
		return model.Database{}, err
	}
	plaintext, err := s.decrypt(ciphertext)
	if err != nil {
		return model.Database{}, err
	}
	var stored model.Database
	if err := json.Unmarshal(plaintext, &stored); err != nil {
		return model.Database{}, err
	}
	database.Password = stored.Password
	if err := model.ValidateDatabase(database); err != nil {
		return model.Database{}, fmt.Errorf("stored database is invalid: %w", err)
	}
	return database, nil
}

func (s *Store) EnqueueDatabaseDelete(ctx context.Context, actor User, id, confirmation string) (string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageServer) {
		return "", errors.New("permission denied")
	}
	database, err := s.Database(ctx, id)
	if err != nil {
		return "", errors.New("database does not exist")
	}
	if confirmation != database.Name {
		return "", errors.New("type the database name exactly to confirm deletion")
	}
	if database.Status != "active" && database.Status != "failed" && database.Status != "delete_failed" {
		return "", errors.New("database cannot be deleted while another operation is running")
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
	result, err := tx.ExecContext(ctx, `UPDATE databases SET status='deleting',updated_at=? WHERE id=? AND status IN ('active','failed','delete_failed')`, now, id)
	if err != nil {
		return "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", errors.New("database changed while deletion was requested")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "database.delete", "database", id, "queued", "waiting", 0, actor.ID, "database.delete:"+id+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "database.delete_requested", "database", id, "success", now); err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) EnqueueDatabaseAdminInstall(ctx context.Context, actor User) (string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageServer) {
		return "", errors.New("permission denied")
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
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM database_admin WHERE id=1`).Scan(&status)
	if err == nil && status != "failed" {
		return "", errors.New("phpMyAdmin is already active or being installed")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_admin(id,status,updated_at) VALUES(1,'queued',?) ON CONFLICT(id) DO UPDATE SET status='queued',updated_at=excluded.updated_at`, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "database.admin_install", "server", "phpmyadmin", "queued", "waiting", 0, actor.ID, "database.admin_install:"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "database_admin.install_requested", "server", "phpmyadmin", "success", now); err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) DatabaseAdminStatus(ctx context.Context) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM database_admin WHERE id=1`).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "not_installed", nil
	}
	return status, err
}

func (s *Store) RecordDatabaseAccess(ctx context.Context, actor User, action, targetType, targetID string) error {
	if !rbac.Allows(actor.Role, rbac.ManageServer) {
		return errors.New("permission denied")
	}
	if action != "database.credentials_viewed" && action != "database.phpmyadmin_opened" {
		return errors.New("invalid database audit action")
	}
	if targetType != "database" && targetType != "site" {
		return errors.New("invalid database audit target")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, action, targetType, targetID, "success", s.now().UTC().Format(time.RFC3339Nano))
	return err
}
