// Package upgrade performs an explicitly requested binary upgrade. The release
// bootstrap verifies the new binary before this package snapshots local state.
package upgrade

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	Source               string
	InstalledPath        string
	ConfigPath           string
	BackupRoot           string
	Runner               Runner
	HealthCheck          func(context.Context, config.Config) error
	ReconcileUnits       func(string) error
	ReconcilePermissions func(config.Config) error
	Now                  func() time.Time
}

type Result struct {
	BackupDirectory string
}

func Run(ctx context.Context, options Options) (Result, error) {
	if os.Geteuid() != 0 {
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
	if options.Runner == nil {
		options.Runner = ExecRunner{}
	}
	if options.HealthCheck == nil {
		options.HealthCheck = panelHealth
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
	for name, path := range map[string]string{"source": source, "installed binary": installed, "configuration": options.ConfigPath, "backup root": options.BackupRoot} {
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
	backupDirectory := filepath.Join(options.BackupRoot, options.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(backupDirectory, 0700); err != nil {
		return Result{}, fmt.Errorf("create upgrade recovery directory: %w", err)
	}
	stopServices := []string{"wpx.service", "wpx-broker.service"}
	startServices := []string{"wpx-broker.service", "wpx.service"}
	if err := options.Runner.Run(ctx, "/usr/bin/systemctl", append([]string{"stop"}, stopServices...)...); err != nil {
		return Result{}, fmt.Errorf("stop WPX for upgrade: %w", err)
	}
	restartOld := func() {
		_ = options.Runner.Run(context.Background(), "/usr/bin/systemctl", append([]string{"start"}, startServices...)...)
	}
	if err := checkpointSQLite(cfg.StatePath); err != nil {
		restartOld()
		return Result{}, err
	}
	binaryBackup, stateBackup := filepath.Join(backupDirectory, "wpx"), filepath.Join(backupDirectory, "state.db")
	if err := copyFile(installed, binaryBackup, 0755); err != nil {
		restartOld()
		return Result{}, fmt.Errorf("back up current binary: %w", err)
	}
	if err := copyFile(cfg.StatePath, stateBackup, 0600); err != nil {
		restartOld()
		return Result{}, fmt.Errorf("back up panel state: %w", err)
	}
	rollback := func(cause error) error {
		_ = options.Runner.Run(context.Background(), "/usr/bin/systemctl", append([]string{"stop"}, stopServices...)...)
		binaryErr := replaceFile(binaryBackup, installed, 0755)
		stateErr := replaceFile(stateBackup, cfg.StatePath, 0600)
		restartOld()
		if binaryErr != nil || stateErr != nil {
			return fmt.Errorf("%v; rollback failed (binary: %v, state: %v); recovery files remain in %s", cause, binaryErr, stateErr, backupDirectory)
		}
		return fmt.Errorf("%v; previous binary and state restored", cause)
	}
	if err := replaceFile(source, installed, 0755); err != nil {
		_ = replaceFile(binaryBackup, installed, 0755)
		_ = replaceFile(stateBackup, cfg.StatePath, 0600)
		restartOld()
		return Result{}, fmt.Errorf("install new WPX binary: %w", err)
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
	return Result{BackupDirectory: backupDirectory}, nil
}

func checkpointSQLite(path string) error {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint panel state: %w", err)
	}
	return nil
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

func panelHealth(ctx context.Context, cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // local self-signed endpoint
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	url := "https://" + net.JoinHostPort(strings.Trim(host, "[]"), port) + "/healthz"
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, requestErr := client.Do(request)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
