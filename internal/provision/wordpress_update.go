package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/model"
)

// UpdateWordPress performs application updates against a sibling copy of the
// live tree. The database rollback survives process interruption until the
// updated copy passes health checks and becomes active.
func (h *Host) UpdateWordPress(ctx context.Context, site model.Site, target model.BackupTarget, update model.WordPressUpdate, jobKey string) (string, error) {
	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress || site.Status != "active" {
		return "", errors.New("update requires an active WordPress site")
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return "", err
	}
	if err := model.ValidateWordPressUpdate(update); err != nil {
		return "", err
	}
	if jobKey == "" || len(jobKey) > 160 {
		return "", errors.New("invalid WordPress update job key")
	}
	wpcli, ok := h.WordPress.(*WPCLI)
	if !ok || h.Runner == nil || !filepath.IsAbs(wpcli.Path) {
		return "", errors.New("WordPress update requires WP-CLI")
	}

	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	recoveryID, err := h.backupSite(ctx, site, target, jobKey+":pre-update")
	if err != nil {
		return "", fmt.Errorf("create pre-update recovery point: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return "", err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))[:16]
	previous := filepath.Join(siteDir, ".wpx-update-previous-"+digest)
	if err := recoverInterruptedFileSwitch(publicDir, previous); err != nil {
		return "", err
	}
	workspace := filepath.Join(siteDir, "tmp", ".wpx-update-"+digest)
	rollbackDatabase := filepath.Join(workspace, "database.sql")
	if err := h.prepareUpdateRollback(ctx, site, identity, publicDir, workspace, rollbackDatabase); err != nil {
		return "", err
	}
	rollbackDB := func(operationErr error) error {
		if rollbackErr := h.runWPDatabase(ctx, site, identity, publicDir, "import", rollbackDatabase); rollbackErr != nil {
			return fmt.Errorf("%v; database rollback also failed: %w", operationErr, rollbackErr)
		}
		return operationErr
	}

	build, err := os.MkdirTemp(siteDir, ".wpx-update-build-")
	if err != nil {
		return "", rollbackDB(err)
	}
	defer os.RemoveAll(build)
	if err := h.Runner.Run(ctx, "/usr/bin/cp", "-a", publicDir+"/.", build+"/"); err != nil {
		return "", rollbackDB(fmt.Errorf("prepare WordPress update copy: %w", err))
	}
	if err := h.runWordPressUpdateCommand(ctx, site, identity, build, update); err != nil {
		return "", rollbackDB(err)
	}
	_ = os.Remove(filepath.Join(build, ".maintenance"))
	if err := h.checkWordPressHealth(ctx, site, identity, build); err != nil {
		return "", rollbackDB(fmt.Errorf("post-update health check: %w", err))
	}
	if err := chownTree(build, identity); err != nil {
		return "", rollbackDB(err)
	}
	if err := os.Rename(publicDir, previous); err != nil {
		return "", rollbackDB(fmt.Errorf("preserve pre-update files: %w", err))
	}
	if err := os.Rename(build, publicDir); err != nil {
		_ = os.Rename(previous, publicDir)
		return "", rollbackDB(fmt.Errorf("activate WordPress update: %w", err))
	}
	// Both cleanup targets are recoverable local copies. Once the live switch
	// succeeds, cleanup failure must not cause the durable job to replay a
	// successful update; a later run removes a stale previous tree first.
	_ = os.RemoveAll(previous)
	_ = os.RemoveAll(workspace)
	return recoveryID, nil
}

func (h *Host) prepareUpdateRollback(ctx context.Context, site model.Site, identity Identity, publicDir, workspace, database string) error {
	if info, err := os.Lstat(database); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("WordPress update rollback database is not a trusted file")
		}
		// A prior worker stopped after mutation began. Restore the original
		// database before replaying the deterministic update.
		if err := h.runWPDatabase(ctx, site, identity, publicDir, "import", database); err != nil {
			return fmt.Errorf("recover interrupted WordPress update database: %w", err)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		return fmt.Errorf("prepare WordPress update rollback: %w", err)
	}
	if err := os.Chown(workspace, identity.UID, identity.GID); err != nil {
		return err
	}
	if err := h.runWPDatabase(ctx, site, identity, publicDir, "export", database); err != nil {
		return fmt.Errorf("export pre-update database: %w", err)
	}
	return nil
}

func (h *Host) runWordPressUpdateCommand(ctx context.Context, site model.Site, identity Identity, publicDir string, update model.WordPressUpdate) error {
	wpcli := h.WordPress.(*WPCLI)
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color"}
	run := func(arguments ...string) error {
		return h.Runner.Run(ctx, "/usr/sbin/runuser", append(base, arguments...)...)
	}
	switch update.Component {
	case model.WordPressCore:
		if err := run("core", "update"); err != nil {
			return fmt.Errorf("update WordPress core: %w", err)
		}
		if err := run("core", "update-db"); err != nil {
			return fmt.Errorf("update WordPress database schema: %w", err)
		}
	case model.WordPressPlugin:
		if err := run("plugin", "update", update.Name); err != nil {
			return fmt.Errorf("update plugin %s: %w", update.Name, err)
		}
	case model.WordPressTheme:
		if err := run("theme", "update", update.Name); err != nil {
			return fmt.Errorf("update theme %s: %w", update.Name, err)
		}
	default:
		return errors.New("unsupported WordPress update component")
	}
	return nil
}
