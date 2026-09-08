// Package upgrade performs an explicitly requested binary upgrade. The release
// bootstrap verifies the new binary before this package snapshots local state.
package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/config"
	"github.com/lum1t4/wpx/internal/install"
	_ "modernc.org/sqlite"
)

type Runner interface {
	Run(context.Context, string, ...string) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, executable string, args ...string) error {
	if err := exec.CommandContext(ctx, executable, args...).Run(); err != nil {
		return fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	return nil
}

type Options struct {
	Source                        string
	InstalledPath                 string
	ConfigPath                    string
	BackupRoot                    string
	UnitRoot                      string
	EffectiveUID                  func() int
	Runner                        Runner
	HealthCheck                   func(context.Context, config.Config) error
	ReconcileSecurityDependencies func(context.Context, Runner) error
	ReconcileUnits                func(string) error
	ReconcilePermissions          func(config.Config) error
	Now                           func() time.Time
}

type Result struct {
	BackupDirectory string
}

func Run(ctx context.Context, options Options) (Result, error) {
	if options.EffectiveUID == nil {
		options.EffectiveUID = os.Geteuid
	}
	if options.EffectiveUID() != 0 {
		return Result{}, errors.New("upgrade must run as root")
	}
	if options.Source == "" {
		var err error
		options.Source, err = os.Executable()
		if err != nil {
			return Result{}, err
		}
	}
	if options.InstalledPath == "" {
		options.InstalledPath = "/usr/local/bin/wpx"
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "/etc/wpx/config.json"
	}
	if options.BackupRoot == "" {
		options.BackupRoot = "/var/lib/wpx/upgrades"
	}
	if options.UnitRoot == "" {
		options.UnitRoot = "/etc/systemd/system"
	}
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.HealthCheck == nil {
		options.HealthCheck = install.WaitForPanel
	}
	if options.ReconcileSecurityDependencies == nil {
		options.ReconcileSecurityDependencies = reconcileSecurityDependencies
	}
	if options.ReconcileUnits == nil {
		options.ReconcileUnits = install.WriteUnits
	}
	if options.ReconcilePermissions == nil {
		options.ReconcilePermissions = func(cfg config.Config) error {
			if err := os.Chmod(cfg.SiteRoot, 0711); err != nil {
				return fmt.Errorf("reconcile shared site-root traversal: %w", err)
			}
			return install.ReconcileNginxCachePermissions()
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	source, err := filepath.Abs(options.Source)
	if err != nil {
		return Result{}, err
	}
	installed, err := filepath.Abs(options.InstalledPath)
	if err != nil || source == installed {
		return Result{}, errors.New("upgrade must run from a newly downloaded binary")
	}
	for name, path := range map[string]string{"source": source, "installed binary": installed, "configuration": options.ConfigPath, "backup root": options.BackupRoot, "systemd units": options.UnitRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return Result{}, fmt.Errorf("%s path is unsafe", name)
		}
	}
	if info, err := os.Stat(source); err != nil || !info.Mode().IsRegular() {
		return Result{}, errors.New("new WPX binary is unavailable")
	}
	cfg, err := config.Load(options.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	if err := options.ReconcilePermissions(cfg); err != nil {
		return Result{}, err
	}
	// Package installation is deliberately completed before WPX is stopped or
	// its recoverable state is changed. The nftables service is not enabled:
	// Fail2ban owns only the tables/chains created by its packaged action.
	if err := options.ReconcileSecurityDependencies(ctx, options.Runner); err != nil {
		return Result{}, fmt.Errorf("reconcile security dependencies: %w", err)
	}
	backupDirectory := filepath.Join(options.BackupRoot, options.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(backupDirectory, 0700); err != nil {
		return Result{}, fmt.Errorf("create upgrade recovery directory: %w", err)
	}
	unitSnapshots, err := snapshotUnits(options.UnitRoot, backupDirectory)
	if err != nil {
		return Result{}, err
	}
	stopServices := []string{"wpx.service", "wpx-broker.service"}
	startServices := []string{"wpx-broker.service", "wpx.service"}
	restartOld := func(cause error) error {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := options.Runner.Run(recoveryCtx, "/usr/bin/systemctl", append([]string{"start"}, startServices...)...); err != nil {
			return fmt.Errorf("%w; restart previous WPX failed: %v", cause, err)
		}
		return cause
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", append([]string{"stop"}, stopServices...)...); err != nil {
		return Result{}, restartOld(fmt.Errorf("stop WPX for upgrade: %w", err))
	}
	if err := checkpointSQLite(cfg.StatePath); err != nil {
		return Result{}, restartOld(err)
	}
	binaryBackup, stateBackup := filepath.Join(backupDirectory, "wpx"), filepath.Join(backupDirectory, "state.db")
	if err := copyFile(installed, binaryBackup, 0755); err != nil {
		return Result{}, restartOld(fmt.Errorf("back up current binary: %w", err))
	}
	if err := copyFile(cfg.StatePath, stateBackup, 0600); err != nil {
		return Result{}, restartOld(fmt.Errorf("back up panel state: %w", err))
	}
	rollback := func(cause error) error {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		// A failed stop may leave either process writing SQLite. Never replace
		// its database or discard its WAL until systemd confirms both stopped.
		if err := options.Runner.Run(recoveryCtx, "/usr/bin/systemctl", append([]string{"stop"}, stopServices...)...); err != nil {
			return fmt.Errorf("%w; rollback could not stop WPX: %v; recovery files remain in %s", cause, err, backupDirectory)
		}
		binaryErr := replaceFile(binaryBackup, installed, 0755)
		stateErr := restoreState(stateBackup, cfg.StatePath)
		unitErr := restoreUnits(unitSnapshots)
		if unitErr == nil {
			unitErr = options.Runner.Run(recoveryCtx, "/usr/bin/systemctl", "daemon-reload")
		}
		if binaryErr != nil || stateErr != nil || unitErr != nil {
			return fmt.Errorf("%w; rollback failed (binary: %v, state: %v, units: %v); recovery files remain in %s", cause, binaryErr, stateErr, unitErr, backupDirectory)
		}
		if err := options.Runner.Run(recoveryCtx, "/usr/bin/systemctl", append([]string{"start"}, startServices...)...); err != nil {
			return fmt.Errorf("%w; previous binary, state and units restored but restart failed: %v; recovery files remain in %s", cause, err, backupDirectory)
		}
		return fmt.Errorf("%w; previous binary and state restored with previous systemd units", cause)
	}
	if err := replaceFile(source, installed, 0755); err != nil {
		return Result{}, rollback(fmt.Errorf("install new WPX binary: %w", err))
	}
	if err := options.ReconcileUnits(options.ConfigPath); err != nil {
		return Result{}, rollback(fmt.Errorf("reconcile systemd units: %w", err))
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
		return Result{}, rollback(fmt.Errorf("reload systemd units: %w", err))
	}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", append([]string{"start"}, startServices...)...); err != nil {
		return Result{}, rollback(fmt.Errorf("start upgraded WPX: %w", err))
	}
	healthContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := options.HealthCheck(healthContext, cfg); err != nil {
		return Result{}, rollback(fmt.Errorf("upgraded panel health check: %w", err))
	}
	for _, service := range startServices {
		if err := options.Runner.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", service); err != nil {
			return Result{}, rollback(fmt.Errorf("upgraded service %s readiness: %w", service, err))
		}
	}
	return Result{BackupDirectory: backupDirectory}, nil
}

func reconcileSecurityDependencies(ctx context.Context, runner Runner) error {
	info, err := os.Stat("/usr/bin/fail2ban-client")
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect existing fail2ban-client: %w", err)
		}
		return reconcileSecurityDependenciesFromState(ctx, runner, false)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("existing fail2ban-client is not an executable regular file")
	}
	return reconcileSecurityDependenciesFromState(ctx, runner, true)
}

type dependencyCommand struct {
	executable string
	args       []string
}

func reconcileSecurityDependenciesFromState(ctx context.Context, runner Runner, fail2banInstalled bool) error {
	commands := []dependencyCommand{
		{"/usr/bin/apt-get", []string{"update"}},
	}
	// Validate existing operator configuration before apt's maintainer script
	// can restart Fail2ban during an upgrade.
	if fail2banInstalled {
		commands = append(commands, dependencyCommand{"/usr/bin/fail2ban-client", []string{"-t"}})
	}
	commands = append(commands, []dependencyCommand{
		// Do not upgrade nftables here. Ubuntu restarts an already enabled
		// nftables.service on package upgrade, which can load a host-owned
		// /etc/nftables.conf containing "flush ruleset". Installing a missing
		// package does not enable that service.
		{"/usr/bin/apt-get", []string{"install", "-y", "--no-install-recommends", "--no-upgrade", "nftables"}},
		{"/usr/bin/apt-get", []string{"-o", "Dpkg::Options::=--force-confold", "install", "-y", "--no-install-recommends", "fail2ban"}},
		{"/usr/bin/test", []string{"-x", "/usr/sbin/nft"}},
		{"/usr/bin/test", []string{"-r", "/etc/fail2ban/action.d/nftables.conf"}},
		{"/usr/bin/fail2ban-client", []string{"-t"}},
		{"/usr/bin/systemctl", []string{"enable", "--now", "fail2ban.service"}},
		{"/usr/bin/systemctl", []string{"is-active", "--quiet", "fail2ban.service"}},
	}...)
	for _, command := range commands {
		if err := runner.Run(ctx, command.executable, command.args...); err != nil {
			return fmt.Errorf("%s %s: %w", filepath.Base(command.executable), command.args[0], err)
		}
	}
	return nil
}

func checkpointSQLite(path string) error {
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return errors.New("panel state database is unavailable; refusing to create an empty upgrade snapshot")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer database.Close()
	var busy, pages, checkpointed int
	if err := database.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint panel state: %w", err)
	}
	if busy != 0 {
		return errors.New("cannot snapshot panel state while another SQLite connection holds its WAL")
	}
	return nil
}

// The snapshot is a checkpointed database. A failed new process can leave WAL
// frames from its newer schema, which must never be replayed into the snapshot.
// The caller must stop both WPX services before invoking this function.
func restoreState(source, destination string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(destination + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove failed-upgrade SQLite sidecar: %w", err)
		}
	}
	return replaceFile(source, destination, 0600)
}

type unitSnapshot struct {
	path, backup string
	mode         os.FileMode
	existed      bool
}

func snapshotUnits(root, backupRoot string) ([]unitSnapshot, error) {
	var snapshots []unitSnapshot
	for _, name := range []string{"wpx-broker.service", "wpx.service"} {
		snapshot := unitSnapshot{path: filepath.Join(root, name), backup: filepath.Join(backupRoot, name)}
		info, err := os.Lstat(snapshot.path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect systemd unit for recovery: %w", err)
		}
		if err == nil {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("systemd unit %s must be a regular file", snapshot.path)
			}
			snapshot.existed, snapshot.mode = true, info.Mode().Perm()
			if err := copyFile(snapshot.path, snapshot.backup, snapshot.mode); err != nil {
				return nil, fmt.Errorf("back up systemd unit: %w", err)
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func restoreUnits(snapshots []unitSnapshot) error {
	var errs []error
	for _, snapshot := range snapshots {
		if snapshot.existed {
			if err := replaceFile(snapshot.backup, snapshot.path, snapshot.mode); err != nil {
				errs = append(errs, err)
			}
		} else if err := os.Remove(snapshot.path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func replaceFile(source, destination string, mode os.FileMode) error {
	var owner *syscall.Stat_t
	if info, err := os.Stat(destination); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = stat
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".wpx-upgrade-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, destination); err != nil {
		return err
	}
	if owner != nil {
		if err := os.Chown(destination, int(owner.Uid), int(owner.Gid)); err != nil {
			return err
		}
	}
	return nil
}
