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

// TestRestore proves that a retained snapshot can be downloaded and consumed
// without changing the live site. WordPress SQL is imported into a disposable
// database and checked by MariaDB before the database is dropped.
func (h *Host) TestRestore(ctx context.Context, site model.Site, target model.BackupTarget, snapshotID, jobKey string) error {
	if err := model.ValidateSite(site); err != nil || site.Status != "active" {
		return errors.New("restore test requires an active site")
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return err
	}
	if !model.ValidResticSnapshotID(snapshotID) || jobKey == "" || len(jobKey) > 160 {
		return errors.New("invalid restore test request")
	}
	if err := h.validateBackupRuntime(); err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	resultRoot := filepath.Join(h.DataRoot, "restore-test-results")
	resultPath := filepath.Join(resultRoot, digest)
	if result, err := os.ReadFile(resultPath); err == nil && strings.TrimSpace(string(result)) == snapshotID {
		return nil
	}

	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	environment, options, err := h.backupEnvironment(target)
	if err != nil {
		return err
	}
	tmpRoot := filepath.Join(h.DataRoot, "tmp")
	if err := os.MkdirAll(tmpRoot, 0700); err != nil {
		return err
	}
	restoreRoot, err := os.MkdirTemp(tmpRoot, "restore-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(restoreRoot)
	arguments := append(append([]string{}, options...), "restore", snapshotID, "--target", restoreRoot)
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, arguments...); err != nil {
		return fmt.Errorf("download snapshot for restore test: %w", err)
	}
	publicDir := filepath.Join(h.SiteRoot, site.ID, "public")
	restoredPublic := filepath.Join(restoreRoot, strings.TrimPrefix(publicDir, string(filepath.Separator)))
	if info, err := os.Lstat(restoredPublic); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("snapshot does not contain the expected site files")
	}
	if err := validateRestoredTree(restoredPublic); err != nil {
		return err
	}
	if site.Kind == model.WordPress {
		if h.Input == nil || h.Runner == nil {
			return errors.New("WordPress restore testing requires database input support")
		}
		databaseExport, err := findRestoredDatabase(restoreRoot)
		if err != nil {
			return err
		}
		databaseName := "wpx_restore_test_" + digest[:16]
		if err := h.Runner.Run(ctx, "/usr/bin/mariadb", "--protocol=socket", "--user=root", "--execute=DROP DATABASE IF EXISTS `"+databaseName+"`; CREATE DATABASE `"+databaseName+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"); err != nil {
			return fmt.Errorf("create disposable restore-test database: %w", err)
		}
		defer h.Runner.Run(context.Background(), "/usr/bin/mariadb", "--protocol=socket", "--user=root", "--execute=DROP DATABASE IF EXISTS `"+databaseName+"`;")
		input, err := os.Open(databaseExport)
		if err != nil {
			return err
		}
		importErr := h.Input.RunInput(ctx, input, "/usr/bin/mariadb", "--protocol=socket", "--user=root", databaseName)
		closeErr := input.Close()
		if importErr != nil {
			return fmt.Errorf("import disposable restore-test database: %w", importErr)
		}
		if closeErr != nil {
			return closeErr
		}
		if err := h.Runner.Run(ctx, "/usr/bin/mariadb-check", "--protocol=socket", "--user=root", databaseName); err != nil {
			return fmt.Errorf("check disposable restore-test database: %w", err)
		}
	}
	if err := os.MkdirAll(resultRoot, 0700); err != nil {
		return err
	}
	return atomicWrite(resultPath, []byte(snapshotID+"\n"), 0600)
}

func validateRestoredTree(root string) error {
	regularFiles := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil || filepath.IsAbs(target) {
				return errors.New("snapshot contains an unsafe symbolic link")
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
			if err := ensureContained(root, resolved); err != nil {
				return errors.New("snapshot symbolic link escapes the site root")
			}
			return nil
		}
		if entry.Type().IsRegular() {
			regularFiles++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate restored file tree: %w", err)
	}
	if regularFiles == 0 {
		return errors.New("restored site contains no regular files")
	}
	return nil
}
