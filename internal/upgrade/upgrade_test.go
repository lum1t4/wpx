package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/config"
)

type recordingRunner struct{ calls []string }

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) error {
	r.calls = append(r.calls, executable+" "+strings.Join(args, " "))
	return nil
}

func TestUpgradeRejectsUnprivilegedCaller(t *testing.T) {
	_, err := Run(context.Background(), Options{EffectiveUID: func() int { return 1000 }})
	if err == nil || err.Error() != "upgrade must run as root" {
		t.Fatalf("expected root requirement, got %v", err)
	}
}

func TestUpgradeSnapshotsReplacesAndHealthChecks(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "bin", "wpx")
	source := filepath.Join(root, "download", "wpx")
	if err := os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new binary"), 0755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "data", "state.db")
	if err := os.MkdirAll(filepath.Dir(statePath), 0750); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE proof(value TEXT); INSERT INTO proof(value) VALUES('preserved')`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	cfg := config.Default()
	cfg.StatePath = statePath
	cfg.DataRoot = filepath.Join(root, "data")
	cfg.SiteRoot = filepath.Join(root, "sites")
	cfg.RunRoot = filepath.Join(root, "run")
	cfg.BrokerSocket = filepath.Join(root, "run", "broker.sock")
	cfg.TLSCertPath = filepath.Join(root, "tls", "panel.crt")
	cfg.TLSKeyPath = filepath.Join(root, "tls", "panel.key")
	cfg.SecretKeyPath = filepath.Join(root, "secret.key")
	if err := os.MkdirAll(cfg.SiteRoot, 0750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "etc", "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	result, err := Run(context.Background(), Options{
		Source: source, InstalledPath: installed, ConfigPath: configPath, BackupRoot: filepath.Join(root, "backups"), Runner: runner,
		EffectiveUID:         func() int { return 0 },
		Now:                  func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) },
		HealthCheck:          func(context.Context, config.Config) error { return nil },
		ReconcileUnits:       func(string) error { return nil },
		ReconcilePermissions: func(cfg config.Config) error { return os.Chmod(cfg.SiteRoot, 0711) },
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(installed)
	if err != nil || string(content) != "new binary" {
		t.Fatalf("installed=%q err=%v", content, err)
	}
	old, err := os.ReadFile(filepath.Join(result.BackupDirectory, "wpx"))
	if err != nil || string(old) != "old binary" {
		t.Fatalf("backup=%q err=%v", old, err)
	}
	if _, err := os.Stat(filepath.Join(result.BackupDirectory, "state.db")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.SiteRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0711 {
		t.Fatalf("site root mode=%v", info.Mode().Perm())
	}
	commands := strings.Join(runner.calls, "\n")
	if !strings.Contains(commands, "systemctl stop wpx.service wpx-broker.service") || !strings.Contains(commands, "systemctl start wpx-broker.service wpx.service") {
		t.Fatalf("service boundary missing: %s", commands)
	}
}

func TestUpgradeRollsBackBinaryAndStateAfterFailedHealthCheck(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "bin", "wpx")
	source := filepath.Join(root, "download", "wpx")
	for _, directory := range []string{filepath.Dir(installed), filepath.Dir(source), filepath.Join(root, "data")} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(installed, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new binary"), 0755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "data", "state.db")
	database, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE proof(value TEXT); INSERT INTO proof(value) VALUES('before')`); err != nil {
		t.Fatal(err)
	}
	database.Close()

	cfg := config.Default()
	cfg.StatePath = statePath
	cfg.DataRoot = filepath.Join(root, "data")
	cfg.SiteRoot = filepath.Join(root, "sites")
	cfg.RunRoot = filepath.Join(root, "run")
	cfg.BrokerSocket = filepath.Join(root, "run", "broker.sock")
	cfg.TLSCertPath = filepath.Join(root, "tls", "panel.crt")
	cfg.TLSKeyPath = filepath.Join(root, "tls", "panel.key")
	cfg.SecretKeyPath = filepath.Join(root, "secret.key")
	if err := os.MkdirAll(cfg.SiteRoot, 0750); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "etc", "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	_, err = Run(context.Background(), Options{
		Source: source, InstalledPath: installed, ConfigPath: configPath, BackupRoot: filepath.Join(root, "backups"), Runner: runner,
		EffectiveUID: func() int { return 0 },
		HealthCheck: func(context.Context, config.Config) error {
			database, openErr := sql.Open("sqlite", statePath)
			if openErr != nil {
				return openErr
			}
			defer database.Close()
			_, _ = database.Exec(`UPDATE proof SET value = 'after'`)
			return errors.New("unhealthy")
		},
		ReconcileUnits:       func(string) error { return nil },
		ReconcilePermissions: func(cfg config.Config) error { return os.Chmod(cfg.SiteRoot, 0711) },
	})
	if err == nil || !strings.Contains(err.Error(), "previous binary and state restored") {
		t.Fatalf("expected successful rollback, got %v", err)
	}
	content, readErr := os.ReadFile(installed)
	if readErr != nil || string(content) != "old binary" {
		t.Fatalf("installed=%q err=%v", content, readErr)
	}
	database, err = sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow(`SELECT value FROM proof`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "before" {
		t.Fatalf("state was not restored: %q", value)
	}
}
