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

type recordingRunner struct {
	calls []string
	fail  func(string) error
}

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) error {
	call := executable + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.fail != nil {
		return r.fail(call)
	}
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
		Source: source, InstalledPath: installed, ConfigPath: configPath, BackupRoot: filepath.Join(root, "backups"), UnitRoot: filepath.Join(root, "units"), Runner: runner,
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
	securityCommands := []string{
		"/usr/bin/apt-get update",
		"/usr/bin/apt-get install -y --no-install-recommends --no-upgrade nftables",
		"/usr/bin/apt-get -o Dpkg::Options::=--force-confold install -y --no-install-recommends fail2ban",
		"/usr/bin/test -x /usr/sbin/nft",
		"/usr/bin/test -r /etc/fail2ban/action.d/nftables.conf",
		"/usr/bin/fail2ban-client -t",
		"/usr/bin/systemctl enable --now fail2ban.service",
		"/usr/bin/systemctl is-active --quiet fail2ban.service",
	}
	stop := strings.Index(commands, "/usr/bin/systemctl stop wpx.service wpx-broker.service")
	for _, command := range securityCommands {
		index := strings.Index(commands, command)
		if index < 0 || index > stop {
			t.Fatalf("security dependency command must complete before WPX stops: %q in\n%s", command, commands)
		}
	}
	if strings.Contains(commands, "nftables.service") || strings.Contains(commands, "/etc/nftables.conf") || strings.Contains(commands, "flush ruleset") {
		t.Fatalf("upgrade must not activate or replace the host nftables ruleset:\n%s", commands)
	}
}

func TestReconcileSecurityDependenciesUsesFixedConservativeCommands(t *testing.T) {
	runner := &recordingRunner{}
	if err := reconcileSecurityDependenciesFromState(context.Background(), runner, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/apt-get update",
		"/usr/bin/apt-get install -y --no-install-recommends --no-upgrade nftables",
		"/usr/bin/apt-get -o Dpkg::Options::=--force-confold install -y --no-install-recommends fail2ban",
		"/usr/bin/test -x /usr/sbin/nft",
		"/usr/bin/test -r /etc/fail2ban/action.d/nftables.conf",
		"/usr/bin/fail2ban-client -t",
		"/usr/bin/systemctl enable --now fail2ban.service",
		"/usr/bin/systemctl is-active --quiet fail2ban.service",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("dependency commands=%q, want %q", runner.calls, want)
	}
	commands := strings.Join(runner.calls, "\n")
	for _, forbidden := range []string{"nftables.service", "/etc/nftables.conf", "nft flush", "iptables", "sshd_config", "jail.local"} {
		if strings.Contains(commands, forbidden) {
			t.Fatalf("dependency reconciliation crossed host policy boundary %q: %s", forbidden, commands)
		}
	}
}

func TestExistingFail2banConfigurationIsValidatedBeforePackageUpgrade(t *testing.T) {
	runner := &recordingRunner{fail: func(call string) error {
		if call == "/usr/bin/fail2ban-client -t" {
			return errors.New("invalid operator jail")
		}
		return nil
	}}
	err := reconcileSecurityDependenciesFromState(context.Background(), runner, true)
	if err == nil || !strings.Contains(err.Error(), "fail2ban-client -t") {
		t.Fatalf("error=%v", err)
	}
	want := []string{"/usr/bin/apt-get update", "/usr/bin/fail2ban-client -t"}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("invalid existing configuration reached package mutation: %v", runner.calls)
	}
}

func TestSecurityDependencyFailureLeavesRunningUpgradeUntouched(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "bin", "wpx")
	source := filepath.Join(root, "download", "wpx")
	statePath := filepath.Join(root, "data", "state.db")
	for _, directory := range []string{filepath.Dir(installed), filepath.Dir(source), filepath.Dir(statePath), filepath.Join(root, "sites")} {
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
	database, err := sql.Open("sqlite", statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE proof(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	cfg := config.Default()
	cfg.StatePath = statePath
	cfg.SiteRoot = filepath.Join(root, "sites")
	configPath := filepath.Join(root, "etc", "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	_, err = Run(context.Background(), Options{
		Source: source, InstalledPath: installed, ConfigPath: configPath,
		BackupRoot: filepath.Join(root, "backups"), UnitRoot: filepath.Join(root, "units"),
		Runner: runner, EffectiveUID: func() int { return 0 },
		ReconcilePermissions:          func(config.Config) error { return nil },
		ReconcileSecurityDependencies: func(context.Context, Runner) error { return errors.New("invalid fail2ban configuration") },
	})
	if err == nil || !strings.Contains(err.Error(), "reconcile security dependencies") {
		t.Fatalf("error=%v", err)
	}
	content, readErr := os.ReadFile(installed)
	if readErr != nil || string(content) != "old binary" {
		t.Fatalf("active binary changed: %q err=%v", content, readErr)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("WPX service operation ran after dependency failure: %v", runner.calls)
	}
	if _, statErr := os.Stat(filepath.Join(root, "backups")); !os.IsNotExist(statErr) {
		t.Fatalf("recovery state should not be created before dependency validation: %v", statErr)
	}
}

func TestUpgradeRollsBackBinaryStateAndUnitsAfterFailedHealthCheck(t *testing.T) {
	for _, failure := range []string{"health", "broker-readiness", "panel-readiness", "rollback-stop", "rollback-start"} {
		t.Run(failure, func(t *testing.T) { testUpgradeRollback(t, failure) })
	}
}

func testUpgradeRollback(t *testing.T, failure string) {
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
	unitRoot := filepath.Join(root, "units")
	if err := os.MkdirAll(unitRoot, 0755); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitRoot, "wpx.service")
	if err := os.WriteFile(unitPath, []byte("old unit"), 0644); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{}
	stopCalls, startCalls := 0, 0
	runner.fail = func(call string) error {
		if strings.Contains(call, "systemctl is-active") && len(strings.Fields(call)) != 4 {
			t.Fatalf("readiness must check one unit at a time: %s", call)
		}
		if failure == "broker-readiness" && call == "/usr/bin/systemctl is-active --quiet wpx-broker.service" || failure == "panel-readiness" && call == "/usr/bin/systemctl is-active --quiet wpx.service" {
			return errors.New("service unavailable")
		}
		if strings.Contains(call, "systemctl stop ") {
			stopCalls++
			if failure == "rollback-stop" && stopCalls == 2 {
				return errors.New("stop failed")
			}
		}
		if strings.Contains(call, "systemctl start ") {
			startCalls++
			if failure == "rollback-start" && startCalls == 2 {
				return errors.New("start failed")
			}
		}
		return nil
	}
	_, err = Run(context.Background(), Options{
		Source: source, InstalledPath: installed, ConfigPath: configPath, BackupRoot: filepath.Join(root, "backups"), UnitRoot: unitRoot, Runner: runner,
		EffectiveUID: func() int { return 0 },
		HealthCheck: func(context.Context, config.Config) error {
			database, openErr := sql.Open("sqlite", statePath)
			if openErr != nil {
				return openErr
			}
			defer database.Close()
			_, _ = database.Exec(`UPDATE proof SET value = 'after'`)
			if strings.HasSuffix(failure, "-readiness") {
				return nil
			}
			return errors.New("unhealthy")
		},
		ReconcileUnits: func(string) error {
			if err := os.WriteFile(unitPath, []byte("new unit"), 0644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(unitRoot, "wpx-broker.service"), []byte("newly created unit"), 0644)
		},
		ReconcilePermissions: func(cfg config.Config) error { return os.Chmod(cfg.SiteRoot, 0711) },
	})
	if err == nil {
		t.Fatal("failed health check must not report a successful upgrade")
	}
	expectedBinary, expectedState, expectedUnit := "old binary", "before", "old unit"
	switch failure {
	case "health", "broker-readiness", "panel-readiness":
		if !strings.Contains(err.Error(), "previous binary and state restored") {
			t.Fatalf("expected successful rollback, got %v", err)
		}
	case "rollback-stop":
		if !strings.Contains(err.Error(), "rollback could not stop WPX") {
			t.Fatalf("expected explicit rollback stop failure, got %v", err)
		}
		expectedBinary, expectedState, expectedUnit = "new binary", "after", "new unit"
	case "rollback-start":
		if !strings.Contains(err.Error(), "restored but restart failed") {
			t.Fatalf("expected explicit rollback restart failure, got %v", err)
		}
	}
	content, readErr := os.ReadFile(installed)
	if readErr != nil || string(content) != expectedBinary {
		t.Fatalf("installed=%q err=%v", content, readErr)
	}
	unit, readErr := os.ReadFile(unitPath)
	if readErr != nil || string(unit) != expectedUnit {
		t.Fatalf("unit=%q err=%v", unit, readErr)
	}
	if _, err := os.Stat(filepath.Join(unitRoot, "wpx-broker.service")); failure != "rollback-stop" && !os.IsNotExist(err) {
		t.Fatalf("unit created during failed upgrade was not removed: %v", err)
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
	if value != expectedState {
		t.Fatalf("state was not restored: %q", value)
	}
}

func TestRestoredDatabaseCannotReplayFailedUpgradeWAL(t *testing.T) {
	root := t.TempDir()
	snapshot, state := filepath.Join(root, "snapshot.db"), filepath.Join(root, "state.db")
	if err := os.WriteFile(snapshot, []byte("checkpointed database"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(state+suffix, []byte("failed upgrade state"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := restoreState(snapshot, state); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(state + suffix); !os.IsNotExist(err) {
			t.Fatalf("stale %s survived rollback: %v", suffix, err)
		}
	}
	content, err := os.ReadFile(state)
	if err != nil || string(content) != "checkpointed database" {
		t.Fatalf("state=%q err=%v", content, err)
	}
}

func TestCheckpointRequiresExistingStateAndExclusiveWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := checkpointSQLite(path); err == nil {
		t.Fatal("missing state must not become an empty snapshot")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("checkpoint created missing state: %v", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE proof(value TEXT); INSERT INTO proof VALUES('before')`); err != nil {
		t.Fatal(err)
	}
	reader, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var value string
	if err := reader.QueryRow(`SELECT value FROM proof`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE proof SET value = 'after'`); err != nil {
		t.Fatal(err)
	}
	if err := checkpointSQLite(path); err == nil {
		t.Fatal("checkpoint must refuse an active reader retaining the previous WAL")
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := checkpointSQLite(path); err != nil {
		t.Fatalf("state should checkpoint once readers stop: %v", err)
	}
}
