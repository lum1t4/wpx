package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

// RestoreClone materializes a snapshot into an independently managed site. It
// provisions destination credentials first, then preserves them while it
// replaces the generated content with the restored source.
func (h *Host) RestoreClone(ctx context.Context, source, target model.Site, backupTarget model.BackupTarget, snapshotID, username, password, jobKey string) error {
	if err := model.ValidateSite(source); err != nil {
		return err
	}
	if err := model.ValidateSite(target); err != nil {
		return err
	}
	if err := model.ValidateBackupTarget(backupTarget); err != nil {
		return err
	}
	if source.Status != "active" || target.Status != "queued" || source.Kind != target.Kind || source.ID == target.ID || !model.ValidResticSnapshotID(snapshotID) || jobKey == "" || len(jobKey) > 160 {
		return errors.New("invalid restore clone relationship")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	resultRoot := filepath.Join(h.DataRoot, "restore-clone-results")
	resultPath := filepath.Join(resultRoot, digest)
	if result, err := os.ReadFile(resultPath); err == nil && strings.TrimSpace(string(result)) == target.ID {
		return nil
	}
	if target.Environment == "staging" {
		if target.Kind != model.WordPress || username != "wpx" || len(password) < 24 || len(password) > 128 {
			return errors.New("invalid protected staging restore")
		}
		if err := h.installStagingAuthentication(ctx, target, username, password); err != nil {
			return err
		}
	}
	if err := h.Provision(ctx, target); err != nil {
		return fmt.Errorf("provision restore destination: %w", err)
	}
	if err := h.validateBackupRuntime(); err != nil {
		return err
	}

	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	environment, options, err := h.backupEnvironment(backupTarget)
	if err != nil {
		return err
	}
	targetDir := filepath.Join(h.SiteRoot, target.ID)
	targetPublic := filepath.Join(targetDir, "public")
	sourcePublic := filepath.Join(h.SiteRoot, source.ID, "public")
	if err := ensureContained(h.SiteRoot, targetDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, target, targetDir)
	if err != nil {
		return err
	}
	restoreRoot, err := os.MkdirTemp(targetDir, ".wpx-restore-clone-")
	if err != nil {
		return fmt.Errorf("prepare clone restore workspace: %w", err)
	}
	defer os.RemoveAll(restoreRoot)
	restoreArguments := append(append([]string{}, options...), "restore", snapshotID, "--target", restoreRoot)
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, restoreArguments...); err != nil {
		return fmt.Errorf("download encrypted snapshot: %w", err)
	}
	restoredSource := filepath.Join(restoreRoot, strings.TrimPrefix(sourcePublic, string(filepath.Separator)))
	if info, err := os.Lstat(restoredSource); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("snapshot does not contain the source site's public files")
	}
	build, err := os.MkdirTemp(targetDir, ".wpx-restore-clone-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(build)
	if err := h.Runner.Run(ctx, "/usr/bin/cp", "-a", restoredSource+"/.", build+"/"); err != nil {
		return fmt.Errorf("stage restored files: %w", err)
	}
	for _, path := range magicLoginFiles(build) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	var rollbackDatabase string
	if target.Kind == model.WordPress {
		configuration, err := os.ReadFile(filepath.Join(targetPublic, "wp-config.php"))
		if err != nil {
			return fmt.Errorf("preserve destination database credentials: %w", err)
		}
		if err := atomicWrite(filepath.Join(build, "wp-config.php"), configuration, 0640); err != nil {
			return err
		}
		rollbackDatabase, err = h.exportRestoreDatabase(ctx, target, identity, targetPublic, "clone-"+digest[:16])
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(rollbackDatabase))
		restoredDatabase, err := findRestoredDatabase(restoreRoot)
		if err != nil {
			return err
		}
		databaseImport, err := stageDatabaseForSite(targetDir, restoredDatabase, identity, "clone-"+digest[:16])
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(databaseImport))
		if err := chownTree(build, identity); err != nil {
			return err
		}
		if err := h.runWPDatabase(ctx, target, identity, build, "import", databaseImport); err != nil {
			return fmt.Errorf("import restored WordPress database: %w", err)
		}
		rollbackDB := func(operationErr error) error {
			if rollbackErr := h.runWPDatabase(ctx, target, identity, targetPublic, "import", rollbackDatabase); rollbackErr != nil {
				return fmt.Errorf("%v; destination database rollback also failed: %w", operationErr, rollbackErr)
			}
			return operationErr
		}
		if err := h.configureRestoredWordPress(ctx, source, target, identity, build); err != nil {
			return rollbackDB(err)
		}
		if err := h.WordPress.ApplyRedis(ctx, target, identity, build, target.RedisEnabled); err != nil {
			return rollbackDB(err)
		}
		if err := h.checkRestoredWordPress(ctx, target, identity, build); err != nil {
			return rollbackDB(err)
		}
	}
	if err := chownTree(build, identity); err != nil {
		return err
	}
	previous := filepath.Join(targetDir, ".wpx-restore-clone-previous-"+digest[:16])
	if err := recoverInterruptedFileSwitch(targetPublic, previous); err != nil {
		return err
	}
	if err := os.Rename(targetPublic, previous); err != nil {
		return fmt.Errorf("preserve generated destination files: %w", err)
	}
	if err := os.Rename(build, targetPublic); err != nil {
		_ = os.Rename(previous, targetPublic)
		if rollbackDatabase != "" {
			_ = h.runWPDatabase(ctx, target, identity, targetPublic, "import", rollbackDatabase)
		}
		return fmt.Errorf("activate restored destination: %w", err)
	}
	_ = os.RemoveAll(previous)
	if err := os.MkdirAll(resultRoot, 0700); err != nil {
		return err
	}
	if err := atomicWrite(resultPath, []byte(target.ID+"\n"), 0600); err != nil {
		return fmt.Errorf("record completed restore clone: %w", err)
	}
	return nil
}

func (h *Host) configureRestoredWordPress(ctx context.Context, source, target model.Site, identity Identity, publicDir string) error {
	wpcli := h.WordPress.(*WPCLI)
	run := func(arguments ...string) error {
		base := []string{"--user", identity.Name, "--", "/usr/bin/php" + target.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color"}
		return h.Runner.Run(ctx, "/usr/sbin/runuser", append(base, arguments...)...)
	}
	targetScheme := "http://"
	if target.TLSStatus == "active" {
		targetScheme = "https://"
	}
	for _, sourceScheme := range []string{"http://", "https://"} {
		if err := run("search-replace", sourceScheme+source.Domain, targetScheme+target.Domain, "--all-tables-with-prefix", "--precise", "--skip-columns=guid"); err != nil {
			return fmt.Errorf("replace serialized source URLs: %w", err)
		}
	}
	if err := run("option", "update", "home", targetScheme+target.Domain); err != nil {
		return err
	}
	if err := run("option", "update", "siteurl", targetScheme+target.Domain); err != nil {
		return err
	}
	blogPublic := "1"
	if target.Environment == "staging" {
		blogPublic = "0"
		if err := installStagingGuard(publicDir, identity); err != nil {
			return err
		}
	} else {
		guard := filepath.Join(publicDir, "wp-content", "mu-plugins", "wpx-staging.php")
		if err := os.Remove(guard); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return run("option", "update", "blog_public", blogPublic)
}

func (h *Host) checkRestoredWordPress(ctx context.Context, site model.Site, identity Identity, publicDir string) error {
	wpcli := h.WordPress.(*WPCLI)
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color"}
	for _, command := range [][]string{{"core", "is-installed"}, {"db", "check"}} {
		if err := h.Runner.Run(ctx, "/usr/sbin/runuser", append(base, command...)...); err != nil {
			return fmt.Errorf("restored WordPress health check %s: %w", strings.Join(command, " "), err)
		}
	}
	return nil
}
