package provision

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) InitBackup(ctx context.Context, target model.BackupTarget) error {
	if err := model.ValidateBackupTarget(target); err != nil {
		return err
	}
	if err := h.validateBackupRuntime(); err != nil {
		return err
	}
	environment, options, err := h.backupEnvironment(target)
	if err != nil {
		return err
	}
	probe := append(append([]string{}, options...), "snapshots", "--json", "--latest", "1")
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, probe...); err == nil {
		return nil
	}
	initialize := append(append([]string{}, options...), "init")
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, initialize...); err != nil {
		return fmt.Errorf("initialize encrypted restic repository: %w", err)
	}
	return nil
}

// RestoreSite first creates a remote rollback snapshot, restores into a
// sibling directory, and switches the public tree with same-filesystem
// renames. WordPress also keeps a local database dump until the imported
// database has passed a core health check.
func (h *Host) RestoreSite(ctx context.Context, site model.Site, target model.BackupTarget, snapshotID, jobKey string) (string, error) {
	if err := model.ValidateSite(site); err != nil {
		return "", err
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return "", err
	}
	if !model.ValidResticSnapshotID(snapshotID) || jobKey == "" || len(jobKey) > 160 {
		return "", errors.New("invalid restore request")
	}
	if err := h.validateBackupRuntime(); err != nil {
		return "", err
	}
	jobDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	resultRoot := filepath.Join(h.DataRoot, "restore-results")
	resultPath := filepath.Join(resultRoot, jobDigest)
	if result, err := os.ReadFile(resultPath); err == nil && model.ValidResticSnapshotID(strings.TrimSpace(string(result))) {
		return strings.TrimSpace(string(result)), nil
	}

	// Serialize snapshot creation with the live-tree and database switch so a
	// concurrent backup cannot capture a half-restored site.
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	rollbackID, err := h.backupSite(ctx, site, target, jobKey+":pre-restore")
	if err != nil {
		return "", fmt.Errorf("create pre-restore rollback point: %w", err)
	}
	environment, options, err := h.backupEnvironment(target)
	if err != nil {
		return "", err
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return "", err
	}
	publicDir := filepath.Join(siteDir, "public")
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return "", err
	}
	restoreRoot, err := os.MkdirTemp(siteDir, ".wpx-restore-")
	if err != nil {
		return "", fmt.Errorf("prepare restore workspace: %w", err)
	}
	defer os.RemoveAll(restoreRoot)
	restoreArgs := append(append([]string{}, options...), "restore", snapshotID, "--target", restoreRoot)
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, restoreArgs...); err != nil {
		return "", fmt.Errorf("download encrypted snapshot: %w", err)
	}
	restoredPublic := filepath.Join(restoreRoot, strings.TrimPrefix(publicDir, string(filepath.Separator)))
	if st, err := os.Lstat(restoredPublic); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("snapshot does not contain the expected site files")
	}
	if err := chownTree(restoredPublic, identity); err != nil {
		return "", fmt.Errorf("restore site ownership: %w", err)
	}

	var restoredDatabase string
	if site.Kind == model.WordPress {
		restoredDatabase, err = findRestoredDatabase(restoreRoot)
		if err != nil {
			return "", err
		}
		restoredDatabase, err = stageDatabaseForSite(siteDir, restoredDatabase, identity, "import-"+jobDigest[:16])
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(filepath.Dir(restoredDatabase))
	}
	rollbackDir := filepath.Join(siteDir, ".wpx-before-restore-"+jobDigest[:16])
	if _, err := os.Lstat(rollbackDir); err == nil {
		return "", errors.New("an incomplete restore requires operator recovery")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var rollbackDatabase string
	if site.Kind == model.WordPress {
		rollbackDatabase, err = h.exportRestoreDatabase(ctx, site, identity, publicDir, jobDigest[:16])
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(filepath.Dir(rollbackDatabase))
	}
	if err := os.Rename(publicDir, rollbackDir); err != nil {
		return "", fmt.Errorf("preserve current site files: %w", err)
	}
	if err := os.Rename(restoredPublic, publicDir); err != nil {
		_ = os.Rename(rollbackDir, publicDir)
		return "", fmt.Errorf("activate restored site files: %w", err)
	}
	if site.Kind == model.WordPress {
		if err := h.importAndCheckWordPress(ctx, site, identity, publicDir, restoredDatabase); err != nil {
			databaseRollbackErr := h.importAndCheckWordPress(ctx, site, identity, rollbackDir, rollbackDatabase)
			_ = os.Rename(publicDir, restoredPublic)
			fileRollbackErr := os.Rename(rollbackDir, publicDir)
			if databaseRollbackErr != nil || fileRollbackErr != nil {
				return "", fmt.Errorf("restore failed (%v) and automatic rollback needs recovery (database: %v, files: %v)", err, databaseRollbackErr, fileRollbackErr)
			}
			return "", fmt.Errorf("restore failed and was rolled back: %w", err)
		}
	}
	if err := os.RemoveAll(rollbackDir); err != nil {
		return "", fmt.Errorf("remove replaced site files: %w", err)
	}
	if err := os.MkdirAll(resultRoot, 0700); err != nil {
		return "", err
	}
	if err := atomicWrite(resultPath, []byte(rollbackID+"\n"), 0600); err != nil {
		return "", fmt.Errorf("record completed restore: %w", err)
	}
	return rollbackID, nil
}

func chownTree(root string, identity Identity) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, identity.UID, identity.GID)
	})
}

func findRestoredDatabase(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() && entry.Name() == "database.sql" {
			if found != "" {
				return errors.New("snapshot contains multiple database exports")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("WordPress snapshot does not contain a database export")
	}
	return found, nil
}

func (h *Host) exportRestoreDatabase(ctx context.Context, site model.Site, identity Identity, publicDir, suffix string) (string, error) {
	_, ok := h.WordPress.(*WPCLI)
	if !ok || h.Runner == nil {
		return "", errors.New("WordPress restore requires WP-CLI")
	}
	directory := filepath.Join(siteDirForPublic(publicDir), "tmp", ".wpx-db-rollback-"+suffix)
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", fmt.Errorf("prepare database rollback workspace: %w", err)
	}
	if err := os.Chown(directory, identity.UID, identity.GID); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "database.sql")
	if err := h.runWPDatabase(ctx, site, identity, publicDir, "export", path); err != nil {
		return "", fmt.Errorf("export current database for rollback: %w", err)
	}
	return path, nil
}

func stageDatabaseForSite(siteDir, source string, identity Identity, suffix string) (string, error) {
	directory := filepath.Join(siteDir, "tmp", ".wpx-db-"+suffix)
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", fmt.Errorf("prepare restored database workspace: %w", err)
	}
	if err := os.Chown(directory, identity.UID, identity.GID); err != nil {
		return "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	destination := filepath.Join(directory, "database.sql")
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chown(destination, identity.UID, identity.GID); err != nil {
		return "", err
	}
	return destination, nil
}

func (h *Host) importAndCheckWordPress(ctx context.Context, site model.Site, identity Identity, publicDir, database string) error {
	if err := h.runWPDatabase(ctx, site, identity, publicDir, "import", database); err != nil {
		return err
	}
	wpcli := h.WordPress.(*WPCLI)
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "core", "is-installed"}
	return h.Runner.Run(ctx, "/usr/sbin/runuser", base...)
}

func (h *Host) runWPDatabase(ctx context.Context, site model.Site, identity Identity, publicDir, action, database string) error {
	wpcli, ok := h.WordPress.(*WPCLI)
	if !ok || !filepath.IsAbs(wpcli.Path) {
		return errors.New("WordPress restore requires WP-CLI")
	}
	args := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "db", action, database}
	if action == "export" {
		args = append(args, "--single-transaction", "--quiet")
	}
	return h.Runner.Run(ctx, "/usr/sbin/runuser", args...)
}

func siteDirForPublic(publicDir string) string { return filepath.Dir(publicDir) }

// BackupSite is retry safe. The durable job key is stored as a restic tag, so
// a worker retry after a lost response returns the already-created snapshot.
func (h *Host) BackupSite(ctx context.Context, site model.Site, target model.BackupTarget, retention model.BackupRetention, jobKey string) (broker.BackupSiteResult, error) {
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	snapshotID, err := h.backupSite(ctx, site, target, jobKey)
	if err != nil {
		return broker.BackupSiteResult{}, err
	}
	retained, applied, err := h.applyBackupRetention(ctx, site, target, retention)
	if err != nil {
		return broker.BackupSiteResult{}, err
	}
	return broker.BackupSiteResult{SnapshotID: snapshotID, RetentionApplied: applied, RetainedSnapshotIDs: retained}, nil
}

func (h *Host) applyBackupRetention(ctx context.Context, site model.Site, target model.BackupTarget, retention model.BackupRetention) ([]string, bool, error) {
	if err := model.ValidateBackupRetention(retention, true); err != nil {
		return nil, false, err
	}
	if retention.KeepDaily+retention.KeepWeekly+retention.KeepMonthly == 0 {
		return nil, false, nil
	}
	environment, options, err := h.backupEnvironment(target)
	if err != nil {
		return nil, false, err
	}
	arguments := append(append([]string{}, options...), "forget", "--tag", "site:"+site.ID, "--group-by", "tags", "--keep-daily", fmt.Sprint(retention.KeepDaily), "--keep-weekly", fmt.Sprint(retention.KeepWeekly), "--keep-monthly", fmt.Sprint(retention.KeepMonthly), "--prune")
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, arguments...); err != nil {
		return nil, false, fmt.Errorf("apply backup retention: %w", err)
	}
	listArguments := append(append([]string{}, options...), "snapshots", "--json", "--tag", "site:"+site.ID)
	output, err := h.Environment.OutputEnv(ctx, environment, h.ResticPath, listArguments...)
	if err != nil {
		return nil, false, fmt.Errorf("list retained backups: %w", err)
	}
	var snapshots []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(output, &snapshots) != nil {
		return nil, false, errors.New("restic returned an invalid retained snapshot list")
	}
	ids := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !model.ValidResticSnapshotID(snapshot.ID) {
			return nil, false, errors.New("restic returned an invalid retained snapshot id")
		}
		ids = append(ids, snapshot.ID)
	}
	return ids, true, nil
}

func (h *Host) backupSite(ctx context.Context, site model.Site, target model.BackupTarget, jobKey string) (string, error) {
	if err := model.ValidateSite(site); err != nil {
		return "", err
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return "", err
	}
	if jobKey == "" || len(jobKey) > 160 || strings.ContainsAny(jobKey, "\r\n") {
		return "", errors.New("invalid backup job key")
	}
	if err := h.validateBackupRuntime(); err != nil {
		return "", err
	}
	environment, options, err := h.backupEnvironment(target)
	if err != nil {
		return "", err
	}
	probe := append(append([]string{}, options...), "snapshots", "--json", "--tag", "job:"+jobKey, "--latest", "1")
	if output, err := h.Environment.OutputEnv(ctx, environment, h.ResticPath, probe...); err == nil {
		if snapshotID := snapshotFromList(output); snapshotID != "" {
			if err := h.checkBackupRepository(ctx, environment, options); err != nil {
				return "", err
			}
			return snapshotID, nil
		}
	}

	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return "", err
	}
	publicDir := filepath.Join(siteDir, "public")
	if st, err := os.Lstat(publicDir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("site public directory is unavailable")
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return "", err
	}
	tmpRoot := filepath.Join(h.DataRoot, "tmp")
	if err := os.MkdirAll(tmpRoot, 0700); err != nil {
		return "", fmt.Errorf("prepare backup temporary root: %w", err)
	}
	tmpDir, err := os.MkdirTemp(tmpRoot, "backup-"+site.ID+"-")
	if err != nil {
		return "", fmt.Errorf("prepare backup workspace: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := os.Chown(tmpDir, identity.UID, identity.GID); err != nil {
		return "", fmt.Errorf("own backup workspace: %w", err)
	}
	metadata, err := json.MarshalIndent(site, "", "  ")
	if err != nil {
		return "", err
	}
	metadata = append(metadata, '\n')
	metadataPath := filepath.Join(tmpDir, "site.json")
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		return "", fmt.Errorf("write backup metadata: %w", err)
	}
	if err := os.Chown(metadataPath, identity.UID, identity.GID); err != nil {
		return "", fmt.Errorf("own backup metadata: %w", err)
	}
	if site.Kind == model.WordPress {
		if h.Runner == nil || h.WordPress == nil {
			return "", errors.New("WordPress backup dependencies are unavailable")
		}
		wpcli, ok := h.WordPress.(*WPCLI)
		if !ok || !filepath.IsAbs(wpcli.Path) {
			return "", errors.New("WordPress backup requires WP-CLI")
		}
		args := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "db", "export", filepath.Join(tmpDir, "database.sql"), "--single-transaction", "--quiet"}
		if err := h.Runner.Run(ctx, "/usr/sbin/runuser", args...); err != nil {
			return "", fmt.Errorf("export WordPress database: %w", err)
		}
	}

	args := append(append([]string{}, options...), "backup", "--json", "--quiet",
		"--tag", "wpx", "--tag", "site:"+site.ID, "--tag", "job:"+jobKey,
		"--exclude", "wpx-login-*.php", publicDir, tmpDir)
	output, err := h.Environment.OutputEnv(ctx, environment, h.ResticPath, args...)
	if err != nil {
		return "", fmt.Errorf("create encrypted site snapshot: %w", err)
	}
	snapshotID := snapshotFromBackup(output)
	if !model.ValidResticSnapshotID(snapshotID) {
		return "", errors.New("restic did not return a snapshot identifier")
	}
	if err := h.checkBackupRepository(ctx, environment, options); err != nil {
		return "", err
	}
	return snapshotID, nil
}

func (h *Host) checkBackupRepository(ctx context.Context, environment, options []string) error {
	arguments := append(append([]string{}, options...), "check")
	if err := h.Environment.RunEnv(ctx, environment, h.ResticPath, arguments...); err != nil {
		return fmt.Errorf("verify encrypted backup repository: %w", err)
	}
	return nil
}

func (h *Host) validateBackupRuntime() error {
	if h.Environment == nil {
		return errors.New("backup command runner is unavailable")
	}
	if !filepath.IsAbs(h.ResticPath) {
		return errors.New("restic path must be absolute")
	}
	if _, err := os.Stat(h.ResticPath); err != nil {
		return fmt.Errorf("inspect restic: %w", err)
	}
	return nil
}

func (h *Host) backupEnvironment(target model.BackupTarget) ([]string, []string, error) {
	cache := filepath.Join(h.DataRoot, "cache", "restic")
	if err := os.MkdirAll(cache, 0700); err != nil {
		return nil, nil, err
	}
	environment := []string{
		"RESTIC_REPOSITORY=" + target.RepositoryURL(),
		"RESTIC_PASSWORD=" + target.RepositoryPassword,
		"RESTIC_CACHE_DIR=" + cache,
	}
	switch target.Kind {
	case model.BackupS3:
		environment = append(environment,
			"AWS_ACCESS_KEY_ID="+target.AccessKey,
			"AWS_SECRET_ACCESS_KEY="+target.SecretKey,
			"AWS_DEFAULT_REGION="+target.Region,
		)
		return environment, []string{"-o", "s3.bucket-lookup=" + target.BucketLookup}, nil
	case model.BackupGoogleDrive:
		if !filepath.IsAbs(h.RclonePath) {
			return nil, nil, errors.New("rclone path must be absolute")
		}
		if _, err := os.Stat(h.RclonePath); err != nil {
			return nil, nil, fmt.Errorf("inspect rclone: %w", err)
		}
		environment = append(environment,
			"RCLONE_CONFIG_WPXDRIVE_TYPE=drive",
			"RCLONE_CONFIG_WPXDRIVE_CLIENT_ID="+target.GoogleClientID,
			"RCLONE_CONFIG_WPXDRIVE_CLIENT_SECRET="+target.GoogleClientSecret,
			"RCLONE_CONFIG_WPXDRIVE_SCOPE=drive.file",
			"RCLONE_CONFIG_WPXDRIVE_TOKEN="+target.GoogleToken,
		)
		if target.GoogleSharedDrive != "" {
			environment = append(environment, "RCLONE_CONFIG_WPXDRIVE_TEAM_DRIVE="+target.GoogleSharedDrive)
		}
		return environment, []string{"-o", "rclone.program=" + h.RclonePath}, nil
	default:
		return nil, nil, errors.New("unsupported backup target")
	}
}

func snapshotFromList(output []byte) string {
	var snapshots []struct {
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
	}
	if json.Unmarshal(output, &snapshots) != nil || len(snapshots) == 0 {
		return ""
	}
	if snapshots[0].ID != "" {
		return snapshots[0].ID
	}
	return snapshots[0].ShortID
}

func snapshotFromBackup(output []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event struct {
			SnapshotID string `json:"snapshot_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.SnapshotID != "" {
			return event.SnapshotID
		}
	}
	return ""
}
