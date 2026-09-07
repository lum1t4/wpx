package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"golang.org/x/crypto/bcrypt"
)

const HostingMigration = `CREATE TABLE node_runtimes (
	site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
	entrypoint TEXT NOT NULL,
	args_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(args_json)),
	port INTEGER NOT NULL UNIQUE CHECK(port BETWEEN 1024 AND 65535),
	node_version TEXT NOT NULL CHECK(node_version IN ('24.20.0')),
	status TEXT NOT NULL CHECK(status IN ('queued','active','failed')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE ftp_users (
	id TEXT PRIMARY KEY,
	site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
	username TEXT NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('queued','active','failed','deleting','delete_failed')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX ftp_users_site ON ftp_users(site_id);
CREATE TABLE mail_service (
	id INTEGER PRIMARY KEY CHECK(id=1),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	hostname TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('disabled','queued','active','failed')),
	updated_at TEXT NOT NULL
);`

var ErrNodeRuntimeNotFound = errors.New("Node runtime does not exist")

func (s *Store) ConfigureNodeRuntime(ctx context.Context, actor User, site model.Site, runtime model.NodeRuntime) (string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageAllSites) {
		return "", errors.New("permission denied")
	}
	if err := model.ValidateNodeRuntime(site, runtime); err != nil {
		return "", err
	}
	args, err := json.Marshal(runtime.Arguments)
	if err != nil {
		return "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var persisted model.Site
	if err := tx.QueryRowContext(ctx, `SELECT id,domain,kind,upstream,status FROM sites WHERE id=?`, site.ID).Scan(&persisted.ID, &persisted.Domain, &persisted.Kind, &persisted.Upstream, &persisted.Status); err != nil || persisted.Kind != model.ReverseProxy {
		return "", errors.New("reverse proxy site does not exist")
	}
	if persisted.Status != "active" {
		return "", errors.New("site must be active before configuring Node.js")
	}
	if err := requireHostingSiteIdle(ctx, tx, site.ID); err != nil {
		return "", err
	}
	if err := model.ValidateNodeRuntime(persisted, runtime); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_runtimes(site_id,entrypoint,args_json,port,node_version,status,created_at,updated_at) VALUES(?,?,?,?,?,'queued',?,?) ON CONFLICT(site_id) DO UPDATE SET entrypoint=excluded.entrypoint,args_json=excluded.args_json,port=excluded.port,node_version=excluded.node_version,status='queued',updated_at=excluded.updated_at`, runtime.SiteID, runtime.Entrypoint, string(args), runtime.Port, runtime.NodeVersion, now, now)
	if err != nil {
		return "", fmt.Errorf("save Node runtime: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "hosting.node_apply", "site", site.ID, "queued", "waiting", 0, actor.ID, "hosting.node_apply:"+site.ID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "node.runtime_requested", "site", site.ID, "success", fmt.Sprintf(`{"port":%d,"node_version":%q}`, runtime.Port, runtime.NodeVersion), now); err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) NodeRuntime(ctx context.Context, siteID string) (model.NodeRuntime, error) {
	var runtime model.NodeRuntime
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT site_id,entrypoint,args_json,port,node_version,status,updated_at FROM node_runtimes WHERE site_id=?`, siteID).Scan(&runtime.SiteID, &runtime.Entrypoint, &raw, &runtime.Port, &runtime.NodeVersion, &runtime.Status, &runtime.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return runtime, ErrNodeRuntimeNotFound
	}
	if err != nil {
		return runtime, err
	}
	if err := json.Unmarshal([]byte(raw), &runtime.Arguments); err != nil {
		return runtime, fmt.Errorf("decode Node arguments: %w", err)
	}
	return runtime, nil
}

func (s *Store) FTPUsersForProvision(ctx context.Context, siteID string) ([]model.FTPUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,username,password_hash,status,created_at FROM ftp_users WHERE site_id=? AND status='active' ORDER BY username`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FTPUser
	for rows.Next() {
		var u model.FTPUser
		if err := rows.Scan(&u.ID, &u.SiteID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueFTPUserDelete(ctx context.Context, actor User, siteID, userID string) (string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageAllSites) {
		return "", errors.New("permission denied")
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := requireHostingSiteIdle(ctx, tx, siteID); err != nil {
		return "", err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ftp_users SET status='deleting',updated_at=? WHERE id=? AND site_id=? AND status IN ('active','failed','delete_failed')`, now, userID, siteID)
	if err != nil {
		return "", err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return "", errors.New("FTP user is not available for deletion")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "hosting.ftp_delete", "ftp_user", userID, "queued", "waiting", 0, actor.ID, "hosting.ftp_delete:"+userID+":"+jobID, now, now)
	if err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}

func (s *Store) CreateFTPUser(ctx context.Context, actor User, siteID, username, password string) (model.FTPUser, string, error) {
	if !rbac.Allows(actor.Role, rbac.ManageAllSites) {
		return model.FTPUser{}, "", errors.New("permission denied")
	}
	username = strings.ToLower(strings.TrimSpace(username))
	if len(password) < 12 || len(password) > 72 {
		return model.FTPUser{}, "", errors.New("FTP password must contain 12-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	user := model.FTPUser{SiteID: siteID, Username: username, PasswordHash: string(hash), Status: "queued"}
	if err := model.ValidateFTPUser(user); err != nil {
		return model.FTPUser{}, "", err
	}
	user.ID, err = randomID("ftp_", 12)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	defer tx.Rollback()
	var siteStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sites WHERE id=?`, siteID).Scan(&siteStatus); err != nil {
		return model.FTPUser{}, "", errors.New("site does not exist")
	}
	if siteStatus != "active" {
		return model.FTPUser{}, "", errors.New("site must be active before creating FTP access")
	}
	if err := requireHostingSiteIdle(ctx, tx, siteID); err != nil {
		return model.FTPUser{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ftp_users(id,site_id,username,password_hash,status,created_at,updated_at) VALUES(?,?,?,?,'queued',?,?)`, user.ID, user.SiteID, user.Username, user.PasswordHash, now, now)
	if err != nil {
		return model.FTPUser{}, "", fmt.Errorf("create FTP user: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "hosting.ftp_apply", "ftp_user", user.ID, "queued", "waiting", 0, actor.ID, "hosting.ftp_apply:"+user.ID, now, now)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "ftp.user_requested", "ftp_user", user.ID, "success", fmt.Sprintf(`{"site_id":%q,"username":%q}`, siteID, username), now)
	if err != nil {
		return model.FTPUser{}, "", err
	}
	user.PasswordHash = ""
	return user, jobID, tx.Commit()
}

func (s *Store) FTPUser(ctx context.Context, id string) (model.FTPUser, error) {
	var u model.FTPUser
	err := s.db.QueryRowContext(ctx, `SELECT id,site_id,username,password_hash,status,created_at FROM ftp_users WHERE id=?`, id).Scan(&u.ID, &u.SiteID, &u.Username, &u.PasswordHash, &u.Status, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, errors.New("FTP user does not exist")
	}
	return u, err
}
func (s *Store) FTPUsers(ctx context.Context, siteID string) ([]model.FTPUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,site_id,username,status,created_at FROM ftp_users WHERE site_id=? ORDER BY username`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FTPUser
	for rows.Next() {
		var u model.FTPUser
		if err := rows.Scan(&u.ID, &u.SiteID, &u.Username, &u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ConfigureMailService(ctx context.Context, actor User, hostname string) (string, error) {
	service := model.MailService{Enabled: true, Hostname: strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), ".")), Status: "queued"}
	if err := model.ValidateMailService(service); err != nil {
		return "", err
	}
	if !rbac.Allows(actor.Role, rbac.ManageServer) {
		return "", errors.New("permission denied")
	}
	jobID, err := randomID("job_", 12)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO mail_service(id,enabled,hostname,status,updated_at) VALUES(1,1,?,'queued',?) ON CONFLICT(id) DO UPDATE SET enabled=1,hostname=excluded.hostname,status='queued',updated_at=excluded.updated_at`, service.Hostname, now)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "hosting.mail_apply", "server", "postfix", "queued", "waiting", 0, actor.ID, "hosting.mail_apply:"+jobID, now, now)
	if err != nil {
		return "", err
	}
	return jobID, tx.Commit()
}
func (s *Store) MailService(ctx context.Context) (model.MailService, error) {
	var m model.MailService
	err := s.db.QueryRowContext(ctx, `SELECT enabled,hostname,status,updated_at FROM mail_service WHERE id=1`).Scan(&m.Enabled, &m.Hostname, &m.Status, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.MailService{Status: "disabled"}, nil
	}
	return m, err
}
func requireHostingSiteIdle(ctx context.Context, tx *sql.Tx, siteID string) error {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM sites WHERE id=?`, siteID).Scan(&status); err != nil {
		return errors.New("site does not exist")
	}
	if status != "active" {
		return errors.New("site must be active before changing hosting access")
	}
	var pending int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j WHERE j.status IN ('queued','running') AND ((j.target_type='site' AND j.target_id=?) OR (j.target_type='ftp_user' AND EXISTS(SELECT 1 FROM ftp_users f WHERE f.id=j.target_id AND f.site_id=?)))`, siteID, siteID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("wait for the current site operation to finish")
	}
	return nil
}
func finishHostingJob(ctx context.Context, tx *sql.Tx, job Job, now string, operationErr error) error {
	status := "active"
	if operationErr != nil {
		status = "failed"
	}
	var err error
	switch job.Kind {
	case "hosting.node_apply":
		_, err = tx.ExecContext(ctx, `UPDATE node_runtimes SET status=?,updated_at=? WHERE site_id=? AND NOT EXISTS(SELECT 1 FROM jobs WHERE kind=? AND target_id=? AND id<>? AND status IN ('queued','running'))`, status, now, job.TargetID, job.Kind, job.TargetID, job.ID)
	case "hosting.ftp_apply":
		_, err = tx.ExecContext(ctx, `UPDATE ftp_users SET status=?,updated_at=? WHERE id=? AND NOT EXISTS(SELECT 1 FROM jobs WHERE kind=? AND target_id=? AND id<>? AND status IN ('queued','running'))`, status, now, job.TargetID, job.Kind, job.TargetID, job.ID)
	case "hosting.mail_apply":
		_, err = tx.ExecContext(ctx, `UPDATE mail_service SET status=?,updated_at=? WHERE id=1 AND NOT EXISTS(SELECT 1 FROM jobs WHERE kind=? AND id<>? AND status IN ('queued','running'))`, status, now, job.Kind, job.ID)
	case "hosting.ftp_delete":
		if operationErr == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM ftp_users WHERE id=?`, job.TargetID)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE ftp_users SET status='delete_failed',updated_at=? WHERE id=?`, now, job.TargetID)
		}
	}
	return err
}

func (s *Store) RetryHostingJob(ctx context.Context, jobID, detail string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',error=?,updated_at=? WHERE id=? AND kind IN ('hosting.node_apply','hosting.ftp_apply','hosting.ftp_delete','hosting.mail_apply') AND status='running'`, detail, s.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), jobID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("hosting job is not awaiting broker confirmation")
	}
	return nil
}
