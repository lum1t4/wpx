package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func activeDatabaseTestSite(t *testing.T, state *Store, owner User) {
	t.Helper()
	createActiveDatabaseTestSite(t, state, owner, model.Site{ID: "database-site", Domain: "database.example.com", Kind: model.PHP, PHPVersion: "8.4"})
}

func createActiveDatabaseTestSite(t *testing.T, state *Store, owner User, site model.Site) {
	t.Helper()
	if _, err := state.CreateSite(context.Background(), owner, site); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(context.Background())
	if err != nil || !found || job.TargetID != site.ID {
		t.Fatalf("claim site job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(context.Background(), job, "{}", nil); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDatabasesRespectSiteAssignments(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	createActiveDatabaseTestSite(t, state, owner, model.Site{ID: "assigned-site", Domain: "assigned.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	createActiveDatabaseTestSite(t, state, owner, model.Site{ID: "private-site", Domain: "private.example.com", Kind: model.PHP, PHPVersion: "8.4"})
	collaborator, err := state.CreateUser(ctx, owner, "developer", "a-secure-test-password", rbac.Collaborator, []string{"assigned-site"})
	if err != nil {
		t.Fatal(err)
	}
	customer, err := state.CreateUser(ctx, owner, "customer", "a-secure-test-password", rbac.Customer, []string{"assigned-site"})
	if err != nil {
		t.Fatal(err)
	}
	assigned, _, err := state.CreateDatabase(ctx, collaborator, "assigned-site", "Assigned data")
	if err != nil {
		t.Fatalf("assigned collaborator could not create database: %v", err)
	}
	if _, _, err := state.CreateDatabase(ctx, collaborator, "private-site", "Private data"); err == nil {
		t.Fatal("collaborator created a database for an unassigned site")
	}
	if _, _, err := state.CreateDatabase(ctx, customer, "assigned-site", "Customer data"); err == nil {
		t.Fatal("customer received database management access")
	}
	databases, err := state.ListDatabasesForSite(ctx, "assigned-site")
	if err != nil || len(databases) != 1 || databases[0].ID != assigned.ID {
		t.Fatalf("site databases=%#v err=%v", databases, err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.TargetID != assigned.ID {
		t.Fatalf("claim assigned database job: job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	assigned, err = state.Database(ctx, assigned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueDatabaseDelete(ctx, collaborator, assigned.ID, assigned.Name); err != nil {
		t.Fatalf("assigned collaborator could not delete database: %v", err)
	}
	private, _, err := state.CreateDatabase(ctx, owner, "private-site", "Private data")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RecordDatabaseAccess(ctx, collaborator, "database.credentials_viewed", "database", private.ID); err == nil {
		t.Fatal("collaborator accessed an unassigned database")
	}
	if _, err := state.EnqueueDatabaseDelete(ctx, collaborator, private.ID, private.Name); err == nil {
		t.Fatal("collaborator deleted an unassigned database")
	}
}

func TestDatabaseCapabilityMigrationBackfillsCollaborators(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for index, migration := range migrations {
		if strings.Contains(migration, "INSERT OR IGNORE INTO site_grants(user_id,site_id,capability)") {
			break
		}
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version) VALUES(?)`, index+1); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES('developer','developer',X'01','collaborator','now','now')`,
		`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES('customer','customer',X'01','customer','now','now')`,
		`INSERT INTO sites(id,domain,kind,status,created_at,updated_at) VALUES('legacy-site','legacy.example.com','static','active','now','now')`,
		`INSERT INTO site_grants(user_id,site_id,capability) VALUES('developer','legacy-site','site.view')`,
		`INSERT INTO site_grants(user_id,site_id,capability) VALUES('customer','legacy-site','site.view')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var collaborators, customers int
	if err := state.db.QueryRow(`SELECT COUNT(*) FROM site_grants WHERE user_id='developer' AND capability='site.databases'`).Scan(&collaborators); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow(`SELECT COUNT(*) FROM site_grants WHERE user_id='customer' AND capability='site.databases'`).Scan(&customers); err != nil {
		t.Fatal(err)
	}
	if collaborators != 1 || customers != 0 {
		t.Fatalf("migrated database grants: collaborators=%d customers=%d", collaborators, customers)
	}
}

func TestManagedDatabaseSecretsAndLifecycle(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	activeDatabaseTestSite(t, state, owner)
	public, createJobID, err := state.CreateDatabase(ctx, owner, "database-site", "Application data")
	if err != nil {
		t.Fatal(err)
	}
	if public.Password != "" || public.ID == "" || createJobID == "" {
		t.Fatalf("public database leaked credentials or identifiers: %#v", public)
	}
	stored, err := state.Database(ctx, public.ID)
	if err != nil || stored.Password == "" || stored.Status != "queued" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT config_ciphertext FROM databases WHERE id=?`, public.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(stored.Password)) {
		t.Fatal("database password was stored in plaintext")
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.create" || job.ID != createJobID {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueDatabaseDelete(ctx, owner, public.ID, "wrong"); err == nil {
		t.Fatal("database deletion did not require exact confirmation")
	}
	deleteJobID, err := state.EnqueueDatabaseDelete(ctx, owner, public.ID, stored.Name)
	if err != nil || deleteJobID == "" {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.delete" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Database(ctx, public.ID); err == nil {
		t.Fatal("successfully deleted database remained in panel state")
	}
}

func TestDatabaseAdminInstallStatusTracksDurableJob(t *testing.T) {
	state := openTestStore(t)
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if status, err := state.DatabaseAdminStatus(ctx); err != nil || status != "not_installed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := state.EnqueueDatabaseAdminInstall(ctx, owner); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "database.admin_install" {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", errors.New("network unavailable")); err != nil {
		t.Fatal(err)
	}
	if status, err := state.DatabaseAdminStatus(ctx); err != nil || status != "failed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if _, err := state.EnqueueDatabaseAdminInstall(ctx, owner); err != nil {
		t.Fatalf("failed installation could not be retried: %v", err)
	}
}
