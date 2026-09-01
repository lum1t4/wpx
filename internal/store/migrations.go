package store

import (
	"context"
	"fmt"
)

var migrations = []string{
	`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		password_hash BLOB NOT NULL,
		role TEXT NOT NULL CHECK(role IN ('owner','administrator','collaborator','customer')),
		disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
		totp_secret_ciphertext BLOB,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE UNIQUE INDEX one_owner ON users(role) WHERE role='owner';
	CREATE TABLE sessions (
		token_hash BLOB PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);
	CREATE TABLE sites (
		id TEXT PRIMARY KEY,
		domain TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL CHECK(kind IN ('wordpress','php','python','static','reverse_proxy')),
		php_version TEXT NOT NULL DEFAULT '',
		upstream TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE site_grants (
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		capability TEXT NOT NULL,
		PRIMARY KEY(user_id,site_id,capability)
	);
	CREATE TABLE jobs (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		status TEXT NOT NULL,
		phase TEXT NOT NULL DEFAULT '',
		progress INTEGER NOT NULL DEFAULT 0 CHECK(progress BETWEEN 0 AND 100),
		error TEXT NOT NULL DEFAULT '',
		initiator_id TEXT REFERENCES users(id),
		idempotency_key TEXT NOT NULL UNIQUE,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		finished_at TEXT
	);
	CREATE TABLE audit_events (
		id TEXT PRIMARY KEY,
		actor_id TEXT REFERENCES users(id),
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		result TEXT NOT NULL,
		detail_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL
	);`,
	`ALTER TABLE users ADD COLUMN totp_pending_ciphertext BLOB;
	ALTER TABLE users ADD COLUMN totp_last_counter INTEGER NOT NULL DEFAULT -1;
	CREATE TABLE totp_recovery_codes (
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		code_hash BLOB NOT NULL,
		created_at TEXT NOT NULL,
		used_at TEXT,
		PRIMARY KEY(user_id,code_hash)
	);
	CREATE TABLE login_challenges (
		token_hash BLOB PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);`,
	`ALTER TABLE sites ADD COLUMN php_eol_ack INTEGER NOT NULL DEFAULT 0 CHECK(php_eol_ack IN (0,1));`,
	`ALTER TABLE sites ADD COLUMN tls_status TEXT NOT NULL DEFAULT 'not_configured';`,
	`CREATE TABLE backup_targets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL CHECK(kind IN ('s3','google_drive')),
		status TEXT NOT NULL,
		config_ciphertext BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`ALTER TABLE jobs ADD COLUMN payload_json TEXT NOT NULL DEFAULT '{}';
	ALTER TABLE jobs ADD COLUMN result_json TEXT NOT NULL DEFAULT '{}';
	CREATE TABLE backup_snapshots (
		id TEXT PRIMARY KEY,
		site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		target_id TEXT NOT NULL REFERENCES backup_targets(id) ON DELETE CASCADE,
		restic_snapshot_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		UNIQUE(target_id,restic_snapshot_id)
	);`,
	`ALTER TABLE sites ADD COLUMN environment TEXT NOT NULL DEFAULT 'production' CHECK(environment IN ('production','staging'));
	ALTER TABLE sites ADD COLUMN parent_site_id TEXT REFERENCES sites(id) ON DELETE RESTRICT;
	CREATE INDEX sites_parent_site ON sites(parent_site_id);`,
	`CREATE TABLE staging_credentials (
		site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
		username TEXT NOT NULL,
		password_ciphertext BLOB NOT NULL
	);`,
	`CREATE TABLE dns_providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		kind TEXT NOT NULL CHECK(kind IN ('cloudflare','route53')),
		status TEXT NOT NULL,
		config_ciphertext BLOB NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE TABLE dns_records (
		id TEXT PRIMARY KEY,
		site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		provider_id TEXT NOT NULL REFERENCES dns_providers(id) ON DELETE RESTRICT,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK(type IN ('A','AAAA','CNAME','TXT')),
		value TEXT NOT NULL,
		ttl INTEGER NOT NULL,
		proxied INTEGER NOT NULL DEFAULT 0 CHECK(proxied IN (0,1)),
		remote_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(provider_id,name,type)
	);`,
	`CREATE TABLE backup_schedules (
		id TEXT PRIMARY KEY,
		site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
		target_id TEXT NOT NULL REFERENCES backup_targets(id) ON DELETE CASCADE,
		interval_hours INTEGER NOT NULL CHECK(interval_hours IN (6,24,168)),
		next_run TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(site_id,target_id)
	);`,
	`ALTER TABLE sites ADD COLUMN redis_enabled INTEGER NOT NULL DEFAULT 0 CHECK(redis_enabled IN (0,1));
	ALTER TABLE sites ADD COLUMN fastcgi_cache_enabled INTEGER NOT NULL DEFAULT 0 CHECK(fastcgi_cache_enabled IN (0,1));`,
	`ALTER TABLE backup_schedules ADD COLUMN keep_daily INTEGER NOT NULL DEFAULT 7 CHECK(keep_daily BETWEEN 0 AND 365);
	ALTER TABLE backup_schedules ADD COLUMN keep_weekly INTEGER NOT NULL DEFAULT 4 CHECK(keep_weekly BETWEEN 0 AND 260);
	ALTER TABLE backup_schedules ADD COLUMN keep_monthly INTEGER NOT NULL DEFAULT 6 CHECK(keep_monthly BETWEEN 0 AND 120);`,
	`ALTER TABLE backup_schedules ADD COLUMN restore_test_interval_days INTEGER NOT NULL DEFAULT 0 CHECK(restore_test_interval_days IN (0,7,30));
	ALTER TABLE backup_schedules ADD COLUMN next_restore_test TEXT;
	ALTER TABLE backup_schedules ADD COLUMN last_restore_test_at TEXT;
	ALTER TABLE backup_schedules ADD COLUMN last_restore_test_status TEXT NOT NULL DEFAULT '' CHECK(last_restore_test_status IN ('','passed','failed'));`,
	`ALTER TABLE sites ADD COLUMN wordpress_multisite TEXT NOT NULL DEFAULT '' CHECK(wordpress_multisite IN ('','subdirectories','subdomains'));`,
	`CREATE TABLE update_status (
		id INTEGER PRIMARY KEY CHECK(id=1),
		current_version TEXT NOT NULL,
		latest_version TEXT NOT NULL,
		release_url TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('ok','failed')),
		error TEXT NOT NULL DEFAULT '',
		checked_at TEXT NOT NULL
	);`,
	`CREATE TABLE site_snippets (
		site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
		nginx TEXT NOT NULL DEFAULT '',
		php TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	for i, migration := range migrations {
		version := i + 1
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=?", version).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES(?)", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}
