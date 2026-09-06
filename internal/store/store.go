// Package store persists desired state and security events in SQLite. It does
// not know about HTTP or privileged operations; callers decide policy before
// asking the store to record a transition.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Store struct {
	db        *sql.DB
	now       func() time.Time
	secretKey []byte
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("state database path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	// One writer is intentional. Product operations are durable jobs and do not
	// need concurrent SQLite writers; serialization makes lock behavior explicit.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=FULL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect state database: %w", err)
	}
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ConfigureSecretKey(key []byte) error {
	if len(key) != 32 {
		return errors.New("application secret key must contain exactly 32 bytes")
	}
	s.secretKey = append(s.secretKey[:0], key...)
	return nil
}

func (s *Store) Health(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("quick check returned %q", result)
	}
	return nil
}

func randomID(prefix string, bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

type User struct {
	ID          string
	Username    string
	Role        rbac.Role
	Disabled    bool
	TOTPEnabled bool
	SiteCount   int
	SiteIDs     []string
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

func (s *Store) OwnerExists(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='owner'").Scan(&count)
	return count > 0, err
}

func (s *Store) CreateOwner(ctx context.Context, username, password string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := validateCredentials(username, password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	id, err := randomID("usr_", 12)
	if err != nil {
		return User{}, fmt.Errorf("generate user id: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='owner'").Scan(&count); err != nil {
		return User{}, err
	}
	if count != 0 {
		return User{}, errors.New("an owner already exists")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,role,created_at,updated_at)
		VALUES(?,?,?,?,?,?)`, id, username, hash, rbac.Owner, now, now); err != nil {
		return User{}, fmt.Errorf("insert owner: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), id, "owner.created", "user", id, "success", now); err != nil {
		return User{}, fmt.Errorf("audit owner creation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: rbac.Owner}, nil
}

func validateCredentials(username, password string) error {
	if !usernamePattern.MatchString(username) {
		return errors.New("username must be 3-64 lowercase letters, digits, dots, underscores, or hyphens")
	}
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain 12-72 bytes")
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain 12-72 bytes")
	}
	return nil
}

func (s *Store) CreateUser(ctx context.Context, actor User, username, password string, role rbac.Role, siteIDs []string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := validateCredentials(username, password); err != nil {
		return User{}, err
	}
	if !rbac.ValidRole(role) || role == rbac.Owner {
		return User{}, errors.New("invalid assignable role")
	}
	if actor.Role == rbac.Administrator && role == rbac.Administrator {
		return User{}, errors.New("administrators cannot create another administrator")
	}
	if actor.Role != rbac.Owner && actor.Role != rbac.Administrator {
		return User{}, errors.New("permission denied")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	id, err := randomID("usr_", 12)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, username, hash, role, now, now); err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	assigned := uniqueStrings(siteIDs)
	if role == rbac.Collaborator || role == rbac.Customer {
		for _, siteID := range assigned {
			if err := model.ValidateSiteID(siteID); err != nil {
				return User{}, err
			}
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sites WHERE id=?", siteID).Scan(&exists); err != nil || exists != 1 {
				return User{}, fmt.Errorf("assigned site %q does not exist", siteID)
			}
			for _, capability := range rbac.Capabilities(role) {
				if _, err := tx.ExecContext(ctx, `INSERT INTO site_grants(user_id,site_id,capability) VALUES(?,?,?)`, id, siteID, capability); err != nil {
					return User{}, fmt.Errorf("grant site capability: %w", err)
				}
			}
		}
	} else {
		assigned = nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "user.created", "user", id, "success", fmt.Sprintf(`{"role":%q}`, role), now); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: role, SiteCount: len(assigned)}, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.username,u.role,u.disabled,u.totp_secret_ciphertext IS NOT NULL,
		COUNT(DISTINCT g.site_id),COALESCE(GROUP_CONCAT(DISTINCT g.site_id),'') FROM users u LEFT JOIN site_grants g ON g.user_id=u.id
		GROUP BY u.id,u.username,u.role,u.disabled,u.totp_secret_ciphertext ORDER BY CASE u.role WHEN 'owner' THEN 0 WHEN 'administrator' THEN 1 WHEN 'collaborator' THEN 2 ELSE 3 END,u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		var siteIDs string
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.Disabled, &user.TOTPEnabled, &user.SiteCount, &siteIDs); err != nil {
			return nil, err
		}
		if siteIDs != "" {
			user.SiteIDs = strings.Split(siteIDs, ",")
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) User(ctx context.Context, userID string) (User, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return User{}, err
	}
	for _, user := range users {
		if user.ID == userID {
			return user, nil
		}
	}
	return User{}, errors.New("user does not exist")
}

func (s *Store) UpdateUserAccess(ctx context.Context, actor User, userID string, role rbac.Role, siteIDs []string) error {
	if actor.Role != rbac.Owner && actor.Role != rbac.Administrator {
		return errors.New("permission denied")
	}
	if !rbac.ValidRole(role) || role == rbac.Owner {
		return errors.New("invalid assignable role")
	}
	if actor.ID == userID {
		return errors.New("you cannot change your own role or assignments")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetRole rbac.Role
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, userID).Scan(&targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("user does not exist")
		}
		return err
	}
	if targetRole == rbac.Owner {
		return errors.New("the owner account cannot be changed")
	}
	if actor.Role == rbac.Administrator && (targetRole == rbac.Administrator || role == rbac.Administrator) {
		return errors.New("only the owner can change administrator access")
	}
	assigned := uniqueStrings(siteIDs)
	if role == rbac.Administrator {
		assigned = nil
	}
	for _, siteID := range assigned {
		if err := model.ValidateSiteID(siteID); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id=?`, siteID).Scan(&exists); err != nil || exists != 1 {
			return fmt.Errorf("assigned site %q does not exist", siteID)
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE users SET role=?,updated_at=? WHERE id=?`, role, now, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_grants WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, siteID := range assigned {
		for _, capability := range rbac.Capabilities(role) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO site_grants(user_id,site_id,capability) VALUES(?,?,?)`, userID, siteID, capability); err != nil {
				return fmt.Errorf("grant site capability: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "user.access_updated", "user", userID, "success", fmt.Sprintf(`{"role":%q,"site_count":%d}`, role, len(assigned)), now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserDisabled(ctx context.Context, actor User, userID string, disabled bool) error {
	if actor.Role != rbac.Owner && actor.Role != rbac.Administrator {
		return errors.New("permission denied")
	}
	if actor.ID == userID {
		return errors.New("you cannot disable your own account")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetRole rbac.Role
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id=?`, userID).Scan(&targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("user does not exist")
		}
		return err
	}
	if targetRole == rbac.Owner {
		return errors.New("the owner account cannot be disabled")
	}
	if actor.Role == rbac.Administrator && targetRole == rbac.Administrator {
		return errors.New("administrators cannot change another administrator")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET disabled=?,updated_at=? WHERE id=?`, disabled, now, userID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("user does not exist")
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE user_id=?`, userID); err != nil {
			return err
		}
	}
	action := "user.enabled"
	if disabled {
		action = "user.disabled"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, action, "user", userID, "success", now); err != nil {
		return err
	}
	return tx.Commit()
}

func mustID(prefix string) string {
	id, err := randomID(prefix, 12)
	if err != nil {
		panic("cryptographic random source unavailable: " + err.Error())
	}
	return id
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	var user User
	var hash []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,role,disabled,totp_secret_ciphertext IS NOT NULL FROM users WHERE username=?`, username).
		Scan(&user.ID, &user.Username, &hash, &user.Role, &user.Disabled, &user.TOTPEnabled)
	if err != nil || user.Disabled || bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		// Callers intentionally receive the same result for unknown, disabled, and
		// incorrect credentials. This avoids turning login into an account oracle.
		return User{}, errors.New("invalid credentials")
	}
	return user, nil
}

func (s *Store) ChangePassword(ctx context.Context, user User, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,updated_at=? WHERE id=? AND disabled=0`, hash, now, user.ID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("active user does not exist")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, user.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE user_id=?`, user.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), user.ID, "password.changed", "user", user.ID, "success", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return "", errors.New("invalid session lifetime")
	}
	token, err := randomID("ses_", 32)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,created_at,expires_at)
		VALUES(?,?,?,?)`, digest[:], userID, now.Format(time.RFC3339Nano), now.Add(ttl).Format(time.RFC3339Nano))
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

func (s *Store) ResolveSession(ctx context.Context, token string) (User, error) {
	digest := sha256.Sum256([]byte(token))
	var user User
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.username,u.role,u.disabled,u.totp_secret_ciphertext IS NOT NULL,s.expires_at
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, digest[:]).
		Scan(&user.ID, &user.Username, &user.Role, &user.Disabled, &user.TOTPEnabled, &expires)
	if err != nil || user.Disabled {
		return User{}, errors.New("invalid session")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !s.now().Before(expiresAt) {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", digest[:])
		return User{}, errors.New("invalid session")
	}
	return user, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	digest := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", digest[:])
	return err
}

func (s *Store) CreateSite(ctx context.Context, actor User, site model.Site) (string, error) {
	site.Domain = normalizedSiteDomain(site.Domain)
	if err := model.ValidateSite(site); err != nil {
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
	performanceDefault := site.Kind == model.WordPress
	_, err = tx.ExecContext(ctx, `INSERT INTO sites(id,domain,kind,php_version,upstream,php_eol_ack,status,redis_enabled,fastcgi_cache_enabled,wordpress_multisite,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, site.ID, site.Domain, site.Kind, site.PHPVersion, site.Upstream, site.AllowEOL, "queued", performanceDefault, performanceDefault, site.WordPressMultisite, now, now)
	if err != nil {
		return "", fmt.Errorf("insert site: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.provision", "site", site.ID, "queued", "waiting", 0, actor.ID, "site.provision:"+site.ID, now, now)
	if err != nil {
		return "", fmt.Errorf("enqueue site provisioning: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "site.requested", "site", site.ID, "success", now)
	if err != nil {
		return "", fmt.Errorf("audit site request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) ListSites(ctx context.Context) ([]model.Site, error) {
	return s.listSites(ctx, "", nil)
}

func (s *Store) ListSitesForUser(ctx context.Context, user User) ([]model.Site, error) {
	if rbac.Allows(user.Role, rbac.ManageAllSites) {
		return s.ListSites(ctx)
	}
	return s.listSites(ctx, ` JOIN site_grants g ON g.site_id=s.id AND g.user_id=? AND g.capability=?`, []any{user.ID, rbac.ViewSite})
}

func (s *Store) listSites(ctx context.Context, join string, args []any) ([]model.Site, error) {
	query := `SELECT DISTINCT s.id,s.domain,s.kind,s.php_version,s.upstream,s.php_eol_ack,s.status,s.tls_status,s.created_at,s.environment,COALESCE(s.parent_site_id,''),s.redis_enabled,s.fastcgi_cache_enabled,s.wordpress_multisite FROM sites s` + join + ` ORDER BY s.domain`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []model.Site
	for rows.Next() {
		var site model.Site
		if err := rows.Scan(&site.ID, &site.Domain, &site.Kind, &site.PHPVersion, &site.Upstream, &site.AllowEOL, &site.Status, &site.TLSStatus, &site.CreatedAt, &site.Environment, &site.ParentSiteID, &site.RedisEnabled, &site.FastCGICacheEnabled, &site.WordPressMultisite); err != nil {
			return nil, err
		}
		result = append(result, site)
	}
	return result, rows.Err()
}

func (s *Store) UserCanSite(ctx context.Context, user User, siteID string, capability rbac.Capability) bool {
	if !rbac.Allows(user.Role, capability) {
		return false
	}
	if rbac.Allows(user.Role, rbac.ManageAllSites) {
		return true
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_grants WHERE user_id=? AND site_id=? AND capability=?`, user.ID, siteID, capability).Scan(&count)
	return err == nil && count == 1
}

func (s *Store) Site(ctx context.Context, id string) (model.Site, error) {
	var site model.Site
	err := s.db.QueryRowContext(ctx, `SELECT id,domain,kind,php_version,upstream,php_eol_ack,status,tls_status,created_at,environment,COALESCE(parent_site_id,''),redis_enabled,fastcgi_cache_enabled,wordpress_multisite FROM sites WHERE id=?`, id).
		Scan(&site.ID, &site.Domain, &site.Kind, &site.PHPVersion, &site.Upstream, &site.AllowEOL, &site.Status, &site.TLSStatus, &site.CreatedAt, &site.Environment, &site.ParentSiteID, &site.RedisEnabled, &site.FastCGICacheEnabled, &site.WordPressMultisite)
	if err != nil {
		return model.Site{}, fmt.Errorf("load site: %w", err)
	}
	return site, nil
}

type Job struct {
	ID             string
	Kind           string
	TargetType     string
	TargetID       string
	Status         string
	Phase          string
	Progress       int
	Error          string
	IdempotencyKey string
	PayloadJSON    string
	ResultJSON     string
}

// JobSummary is the deliberately small operator-facing view of a durable job.
// Payloads and raw results stay out of the browser because they may contain
// provider details or short-lived operational material that is not needed to
// answer the useful questions: what ran, for which target, and did it work?
type JobSummary struct {
	ID         string
	Kind       string
	TargetType string
	TargetID   string
	Status     string
	Phase      string
	Progress   int
	Error      string
	Initiator  string
	CreatedAt  string
	UpdatedAt  string
	FinishedAt string
}

func (s *Store) RecentJobsForUser(ctx context.Context, user User, limit int) ([]JobSummary, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := `SELECT j.id,j.kind,j.target_type,j.target_id,j.status,j.phase,j.progress,j.error,
		COALESCE(u.username,'System'),j.created_at,j.updated_at,COALESCE(j.finished_at,'')
		FROM jobs j LEFT JOIN users u ON u.id=j.initiator_id`
	arguments := []any{}
	if !rbac.Allows(user.Role, rbac.ManageAllSites) {
		query += ` WHERE (
			(j.target_type='site' AND EXISTS (
				SELECT 1 FROM site_grants g WHERE g.user_id=? AND g.site_id=j.target_id AND g.capability=?
			)) OR
			(j.target_type='dns_record' AND EXISTS (
				SELECT 1 FROM dns_records d JOIN site_grants g ON g.site_id=d.site_id
				WHERE d.id=j.target_id AND g.user_id=? AND g.capability=?
			))
		)`
		arguments = append(arguments, user.ID, rbac.ViewSite, user.ID, rbac.ViewSite)
	}
	query += ` ORDER BY j.created_at DESC,j.id DESC LIMIT ?`
	arguments = append(arguments, limit)
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list recent jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]JobSummary, 0)
	for rows.Next() {
		var job JobSummary
		if err := rows.Scan(&job.ID, &job.Kind, &job.TargetType, &job.TargetID, &job.Status, &job.Phase, &job.Progress, &job.Error, &job.Initiator, &job.CreatedAt, &job.UpdatedAt, &job.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan recent job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent jobs: %w", err)
	}
	return jobs, nil
}

// ClaimNextJob uses a conditional update even though the current service runs a
// single worker. The condition preserves correctness if a second worker is
// introduced later or an old process overlaps briefly during restart.
func (s *Store) ClaimNextJob(ctx context.Context) (Job, bool, error) {
	var job Job
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,target_type,target_id,status,phase,progress,error,idempotency_key,payload_json,result_json
		FROM jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1`).
		Scan(&job.ID, &job.Kind, &job.TargetType, &job.TargetID, &job.Status, &job.Phase, &job.Progress, &job.Error, &job.IdempotencyKey, &job.PayloadJSON, &job.ResultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='running',phase='starting',progress=1,updated_at=?
		WHERE id=? AND status='queued'`, now, job.ID)
	if err != nil {
		return Job{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Job{}, false, err
	}
	job.Status, job.Phase, job.Progress = "running", "starting", 1
	return job, true, nil
}

// RequeueInterruptedJobs repairs the only state a clean process restart cannot
// finish by itself. Broker operations are idempotent, so replaying the same job
// key converges on the requested host state instead of duplicating it.
func (s *Store) RequeueInterruptedJobs(ctx context.Context) (int64, error) {
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued',phase='waiting',progress=0,
		error='previous worker stopped before reporting completion',updated_at=? WHERE status='running'`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) FinishJob(ctx context.Context, job Job, resultJSON string, operationErr error) error {
	if resultJSON == "" {
		resultJSON = "{}"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	status, phase, progress, errorText, result := "succeeded", "complete", 100, "", "success"
	if operationErr != nil {
		status, phase, progress, errorText, result = "failed", "failed", job.Progress, operationErr.Error(), "failure"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	completion, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,phase=?,progress=?,error=?,result_json=?,updated_at=?,finished_at=? WHERE id=? AND status='running'`,
		status, phase, progress, errorText, resultJSON, now, now, job.ID)
	if err != nil {
		return err
	}
	changed, err := completion.RowsAffected()
	if err != nil || changed == 0 {
		// A completed job must not apply its result again after a later job has
		// changed the site. The transaction rolls back without another audit.
		return err
	}
	if job.Kind == "site.php_version" {
		if err := finishPHPVersionChange(ctx, tx, job, now, operationErr); err != nil {
			return err
		}
	}
	if job.Kind == "site.domain_change" {
		if err := finishDomainChange(ctx, tx, job, now, operationErr); err != nil {
			return err
		}
	}
	if job.Kind == "site.delete" {
		if err := finishSiteDelete(ctx, tx, job, now, operationErr); err != nil {
			return err
		}
	}
	if job.Kind == "site.provision" || job.Kind == "wordpress.staging_create" || job.Kind == "site.restore_clone" {
		siteStatus := "active"
		if operationErr != nil {
			siteStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, siteStatus, now, job.TargetID); err != nil {
			return err
		}
		if operationErr == nil && (job.Kind == "wordpress.staging_create" || job.Kind == "site.restore_clone") {
			if err := enqueueAutomaticStagingCertificate(ctx, tx, job.TargetID, now); err != nil {
				return err
			}
		}
	}
	if job.Kind == "site.disable" {
		siteStatus := "disabled"
		if operationErr != nil {
			siteStatus = "disable_failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, siteStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "site.enable" {
		siteStatus := "active"
		if operationErr != nil {
			siteStatus = "enable_failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET status=?,updated_at=? WHERE id=?`, siteStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "site.certificate" || job.Kind == "site.certificate_dns" {
		tlsStatus := "active"
		if operationErr != nil {
			tlsStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sites SET tls_status=?,updated_at=? WHERE id=?`, tlsStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "backup.target_init" {
		targetStatus := "active"
		if operationErr != nil {
			targetStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_targets SET status=?,updated_at=? WHERE id=?`, targetStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "dns.provider_verify" {
		providerStatus := "active"
		if operationErr != nil {
			providerStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE dns_providers SET status=?,updated_at=? WHERE id=?`, providerStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "dns.record_apply" {
		recordStatus := "active"
		remoteID := ""
		if operationErr != nil {
			recordStatus = "failed"
		} else {
			var output struct {
				RemoteID string `json:"remote_id"`
			}
			if json.Unmarshal([]byte(resultJSON), &output) != nil {
				return errors.New("DNS record result is incomplete")
			}
			remoteID = output.RemoteID
		}
		if operationErr == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE dns_records SET status=?,remote_id=?,updated_at=? WHERE id=?`, recordStatus, remoteID, now, job.TargetID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE dns_records SET status=?,updated_at=? WHERE id=?`, recordStatus, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "dns.record_delete" {
		if operationErr == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM dns_records WHERE id=?`, job.TargetID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE dns_records SET status='delete_failed',updated_at=? WHERE id=?`, now, job.TargetID); err != nil {
			return err
		}
	}
	if job.Kind == "site.backup" && operationErr == nil {
		var payload struct {
			TargetID string `json:"target_id"`
		}
		var output struct {
			SnapshotID          string   `json:"snapshot_id"`
			RetentionApplied    bool     `json:"retention_applied"`
			RetainedSnapshotIDs []string `json:"retained_snapshot_ids"`
		}
		if json.Unmarshal([]byte(job.PayloadJSON), &payload) != nil || json.Unmarshal([]byte(resultJSON), &output) != nil || payload.TargetID == "" || output.SnapshotID == "" {
			return errors.New("backup job result is incomplete")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO backup_snapshots(id,site_id,target_id,restic_snapshot_id,created_at) VALUES(?,?,?,?,?)`, mustID("snp_"), job.TargetID, payload.TargetID, output.SnapshotID, now); err != nil {
			return err
		}
		if output.RetentionApplied {
			retained := make(map[string]struct{}, len(output.RetainedSnapshotIDs))
			for _, id := range output.RetainedSnapshotIDs {
				if !model.ValidResticSnapshotID(id) {
					return errors.New("backup retention result contains an invalid snapshot")
				}
				retained[id] = struct{}{}
			}
			rows, err := tx.QueryContext(ctx, `SELECT id,restic_snapshot_id FROM backup_snapshots WHERE site_id=? AND target_id=?`, job.TargetID, payload.TargetID)
			if err != nil {
				return err
			}
			type obsoleteSnapshot struct{ id, resticID string }
			var obsolete []obsoleteSnapshot
			for rows.Next() {
				var snapshot obsoleteSnapshot
				if err := rows.Scan(&snapshot.id, &snapshot.resticID); err != nil {
					rows.Close()
					return err
				}
				if _, exists := retained[snapshot.resticID]; !exists {
					obsolete = append(obsolete, snapshot)
				}
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, snapshot := range obsolete {
				if _, err := tx.ExecContext(ctx, `DELETE FROM backup_snapshots WHERE id=?`, snapshot.id); err != nil {
					return err
				}
			}
		}
	}
	if (job.Kind == "wordpress.staging_deploy" || job.Kind == "wordpress.update") && operationErr == nil {
		var payload struct {
			TargetID string `json:"target_id"`
		}
		var output struct {
			RecoverySnapshotID string `json:"recovery_snapshot_id"`
		}
		if json.Unmarshal([]byte(job.PayloadJSON), &payload) != nil || json.Unmarshal([]byte(resultJSON), &output) != nil || payload.TargetID == "" || !model.ValidResticSnapshotID(output.RecoverySnapshotID) {
			return errors.New("staging deployment recovery result is incomplete")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO backup_snapshots(id,site_id,target_id,restic_snapshot_id,created_at) VALUES(?,?,?,?,?)`, mustID("snp_"), job.TargetID, payload.TargetID, output.RecoverySnapshotID, now); err != nil {
			return err
		}
	}
	if job.Kind == "site.restore_test" {
		var payload struct {
			ScheduleID string `json:"schedule_id"`
		}
		if json.Unmarshal([]byte(job.PayloadJSON), &payload) != nil {
			return errors.New("restore test result payload is invalid")
		}
		if payload.ScheduleID != "" {
			testStatus := "passed"
			if operationErr != nil {
				testStatus = "failed"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE backup_schedules SET last_restore_test_at=?,last_restore_test_status=?,updated_at=? WHERE id=?`, now, testStatus, now, payload.ScheduleID); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,action,target_type,target_id,result,detail_json,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), job.Kind, job.TargetType, job.TargetID, result, `{"job_id":"`+job.ID+`"}`, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func enqueueAutomaticStagingCertificate(ctx context.Context, tx *sql.Tx, siteID, now string) error {
	var providerID, environment string
	var multisite model.WordPressMultisiteMode
	err := tx.QueryRowContext(ctx, `SELECT r.provider_id,s.environment,s.wordpress_multisite FROM sites s JOIN dns_records r ON r.site_id=s.id JOIN dns_providers p ON p.id=r.provider_id AND p.status='active' WHERE s.id=? AND s.kind='wordpress' AND s.environment='staging' AND r.type IN ('A','AAAA','CNAME') ORDER BY r.name LIMIT 1`, siteID).Scan(&providerID, &environment, &multisite)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil || environment != "staging" {
		return err
	}
	payload, err := json.Marshal(struct {
		ProviderID string `json:"provider_id"`
		Wildcard   bool   `json:"wildcard"`
	}{ProviderID: providerID, Wildcard: multisite == model.MultisiteSubdomains})
	if err != nil {
		return err
	}
	jobID := mustID("job_")
	if _, err := tx.ExecContext(ctx, `UPDATE sites SET tls_status='queued',updated_at=? WHERE id=?`, now, siteID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,idempotency_key,payload_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.certificate_dns", "site", siteID, "queued", "waiting", 0, "site.certificate_dns:auto:"+siteID+":"+jobID, string(payload), now, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,action,target_type,target_id,result,detail_json,created_at) VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), "certificate.dns_automatic", "site", siteID, "success", string(payload), now)
	return err
}

func (s *Store) EnqueueCertificate(ctx context.Context, actor User, siteID string) (string, error) {
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
	result, err := tx.ExecContext(ctx, `UPDATE sites SET tls_status='queued',updated_at=? WHERE id=? AND status='active'`, now, siteID)
	if err != nil {
		return "", err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return "", errors.New("certificate issuance requires an active site")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,target_type,target_id,status,phase,progress,initiator_id,idempotency_key,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, "site.certificate", "site", siteID, "queued", "waiting", 0, actor.ID, "site.certificate:"+siteID+":"+jobID, now, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, mustID("aud_"), actor.ID, "certificate.requested", "site", siteID, "success", now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return jobID, nil
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	var job Job
	err := s.db.QueryRowContext(ctx, `SELECT id,kind,target_type,target_id,status,phase,progress,error,idempotency_key,payload_json,result_json FROM jobs WHERE id=?`, id).
		Scan(&job.ID, &job.Kind, &job.TargetType, &job.TargetID, &job.Status, &job.Phase, &job.Progress, &job.Error, &job.IdempotencyKey, &job.PayloadJSON, &job.ResultJSON)
	return job, err
}
