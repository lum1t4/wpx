package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Exercise the upgrade from the database-management release, rather than only
// creating an empty database with the latest schema. Existing site identity,
// queued work, and opaque encrypted credentials must survive feature additions.
func TestFeatureUpgradePreservesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations {
		if migration == SecurityMigration {
			break
		}
		if _, err := db.Exec(migration); err != nil {
			t.Fatalf("prepare previous schema %d: %v", i+1, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES('owner_old','operator',X'010203','owner','2026-09-01','2026-09-01')`,
		`INSERT INTO sites(id,domain,kind,php_version,status,created_at,updated_at) VALUES('legacy-site','legacy.example.com','wordpress','8.4','active','2026-09-01','2026-09-01')`,
		`INSERT INTO jobs(id,kind,target_type,target_id,status,initiator_id,idempotency_key,created_at,updated_at) VALUES('old_job','site.backup','site','legacy-site','queued','owner_old','original-key','2026-09-01','2026-09-01')`,
		`INSERT INTO backup_targets(id,name,kind,status,config_ciphertext,created_at,updated_at) VALUES('old_target','Existing storage','s3','active',X'0102ff','2026-09-01','2026-09-01')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		state, err := Open(path)
		if err != nil {
			t.Fatalf("open after upgrade, pass %d: %v", pass, err)
		}
		site, err := state.Site(context.Background(), "legacy-site")
		if err != nil || site.Domain != "legacy.example.com" || site.Status != "active" || site.PHPVersion != "8.4" {
			t.Fatalf("site changed during upgrade: %#v, %v", site, err)
		}
		job, err := state.Job(context.Background(), "old_job")
		if err != nil || job.Status != "queued" || job.IdempotencyKey != "original-key" {
			t.Fatalf("queued work changed during upgrade: %#v, %v", job, err)
		}
		var ciphertext string
		if err := state.db.QueryRow(`SELECT hex(config_ciphertext) FROM backup_targets WHERE id='old_target'`).Scan(&ciphertext); err != nil || ciphertext != "0102FF" {
			t.Fatalf("encrypted credentials changed: %q, %v", ciphertext, err)
		}
		var count int
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(migrations) {
			t.Fatalf("migration replay failed: %d, %v", count, err)
		}
		if err := state.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
