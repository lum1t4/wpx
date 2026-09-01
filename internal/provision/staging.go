package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/crypto/bcrypt"
)

// CreateStaging provisions an isolated WordPress runtime and database, copies
// production files while preserving the staging database credentials, then
// performs serialized-safe URL replacement through WP-CLI.
func (h *Host) CreateStaging(ctx context.Context, source, target model.Site, username, password string) error {
	if err := model.ValidateSite(source); err != nil {
		return err
	}
	if err := model.ValidateSite(target); err != nil {
		return err
	}
	if source.Kind != model.WordPress || source.Environment != "production" || source.Status != "active" || target.Kind != model.WordPress || target.Environment != "staging" || target.ParentSiteID != source.ID || target.WordPressMultisite != source.WordPressMultisite {
		return errors.New("invalid production-to-staging relationship")
	}
	if _, ok := h.WordPress.(*WPCLI); !ok || h.Runner == nil {
		return errors.New("staging requires WP-CLI")
	}
	if username != "wpx" || len(password) < 24 || len(password) > 128 {
		return errors.New("invalid staging access credential")
	}
	if err := h.installStagingAuthentication(ctx, target, username, password); err != nil {
		return err
	}
	if err := h.Provision(ctx, target); err != nil {
		return fmt.Errorf("provision staging runtime: %w", err)
	}

	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.replaceWordPressEnvironment(ctx, source, target, true)
}

// DeployStaging creates a portable recovery point before replacing production.
// The local database/file rollback in replaceWordPressEnvironment protects the
// in-flight operation; the restic snapshot protects post-deploy recovery.
func (h *Host) DeployStaging(ctx context.Context, staging, production model.Site, backupTarget model.BackupTarget, selection broker.StagingSelection, jobKey string) (string, error) {
	if err := model.ValidateSite(staging); err != nil {
		return "", err
	}
	if err := model.ValidateSite(production); err != nil {
		return "", err
	}
	if err := model.ValidateBackupTarget(backupTarget); err != nil {
		return "", err
	}
	if err := validateStagingPair(staging, production); err != nil {
		return "", err
	}
	if jobKey == "" || len(jobKey) > 160 {
		return "", errors.New("invalid deployment job key")
	}
	if !selection.Full {
		inspection, err := h.InspectStaging(ctx, staging, production)
		if err != nil {
			return "", err
		}
		if err := validateInspectedSelection(selection, inspection); err != nil {
			return "", err
		}
	}
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	recoveryID, err := h.backupSite(ctx, production, backupTarget, jobKey+":pre-deploy")
	if err != nil {
		return "", fmt.Errorf("create production recovery point: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var deployErr error
	if selection.Full {
		deployErr = h.replaceWordPressEnvironment(ctx, staging, production, false)
	} else {
		deployErr = h.deploySelectedStagingChanges(ctx, staging, production, selection)
	}
	if deployErr != nil {
		return "", deployErr
	}
	return recoveryID, nil
}

func validateInspectedSelection(selection broker.StagingSelection, inspection broker.StagingInspection) error {
	if err := model.ValidateStagingSelection(selection); err != nil || selection.Full {
		return errors.New("invalid custom staging selection")
	}
	// Files are intentionally not required to remain in the diff. A worker may
	// retry after the file switch succeeded but before its response arrived; in
	// that state an added or modified file has converged and a deletion is absent
	// on both sides. Applying the same validated selection again is idempotent.
	availableTables := make(map[string]struct{}, len(inspection.Tables))
	for _, table := range inspection.Tables {
		availableTables[table] = struct{}{}
	}
	for _, table := range selection.Tables {
		if _, ok := availableTables[table]; !ok || !databaseTableName(table) {
			return fmt.Errorf("selected database table %q is unavailable", table)
		}
	}
	return nil
}

func (h *Host) deploySelectedStagingChanges(ctx context.Context, staging, production model.Site, selection broker.StagingSelection) error {
	stagingDir := filepath.Join(h.SiteRoot, staging.ID)
	productionDir := filepath.Join(h.SiteRoot, production.ID)
	stagingPublic := filepath.Join(stagingDir, "public")
	productionPublic := filepath.Join(productionDir, "public")
	stagingIdentity, err := h.Identities.Ensure(ctx, staging, stagingDir)
	if err != nil {
		return err
	}
	productionIdentity, err := h.Identities.Ensure(ctx, production, productionDir)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(staging.ID+":"+production.ID+":"+strings.Join(selection.Tables, ","))))

	var databaseRollback, selectedDatabase string
	if len(selection.Tables) != 0 {
		databaseRollback, err = h.exportRestoreDatabase(ctx, production, productionIdentity, productionPublic, "custom-rollback-"+digest[:16])
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(databaseRollback))
		stagingExport, exportErr := h.exportSelectedTables(ctx, staging, stagingIdentity, stagingPublic, selection.Tables, digest[:16])
		if exportErr != nil {
			return exportErr
		}
		defer os.RemoveAll(filepath.Dir(stagingExport))
		selectedDatabase, err = stageDatabaseForSite(productionDir, stagingExport, productionIdentity, "custom-"+digest[:16])
		if err != nil {
			return err
		}
		defer os.RemoveAll(filepath.Dir(selectedDatabase))
	}

	rollbackDatabase := func(operationErr error) error {
		if databaseRollback == "" {
			return operationErr
		}
		if rollbackErr := h.runWPDatabase(ctx, production, productionIdentity, productionPublic, "import", databaseRollback); rollbackErr != nil {
			return fmt.Errorf("%v; production database rollback also failed: %w", operationErr, rollbackErr)
		}
		return operationErr
	}

	var clonePublic string
	if len(selection.Files) != 0 {
		clonePublic, err = os.MkdirTemp(productionDir, ".wpx-custom-build-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(clonePublic)
		if err := h.Runner.Run(ctx, "/usr/bin/cp", "-a", productionPublic+"/.", clonePublic+"/"); err != nil {
			return fmt.Errorf("prepare custom production file set: %w", err)
		}
		for _, relative := range selection.Files {
			if err := applySelectedFile(stagingPublic, clonePublic, relative); err != nil {
				return fmt.Errorf("stage selected file %q: %w", relative, err)
			}
		}
		if err := chownTree(clonePublic, productionIdentity); err != nil {
			return err
		}
	}

	wpPath := productionPublic
	if clonePublic != "" {
		wpPath = clonePublic
	}
	if selectedDatabase != "" {
		if err := h.runWPDatabase(ctx, production, productionIdentity, wpPath, "import", selectedDatabase); err != nil {
			return rollbackDatabase(fmt.Errorf("import selected staging tables: %w", err))
		}
		if err := h.replaceURLsInTables(ctx, staging, production, productionIdentity, wpPath, selection.Tables); err != nil {
			return rollbackDatabase(err)
		}
	}
	if err := h.checkWordPressHealth(ctx, production, productionIdentity, wpPath); err != nil {
		return rollbackDatabase(fmt.Errorf("custom deployment health check: %w", err))
	}
	if clonePublic == "" {
		return nil
	}
	rollbackPublic := filepath.Join(productionDir, ".wpx-custom-previous-"+digest[:16])
	if err := recoverInterruptedFileSwitch(productionPublic, rollbackPublic); err != nil {
		return rollbackDatabase(err)
	}
	if err := os.Rename(productionPublic, rollbackPublic); err != nil {
		return rollbackDatabase(fmt.Errorf("preserve production files: %w", err))
	}
	if err := os.Rename(clonePublic, productionPublic); err != nil {
		_ = os.Rename(rollbackPublic, productionPublic)
		return rollbackDatabase(fmt.Errorf("activate selected staging files: %w", err))
	}
	// The active switch is already committed. Cleanup is retried before the next
	// switch and must not turn a successful deployment into a replayed failure.
	_ = os.RemoveAll(rollbackPublic)
	return nil
}

func recoverInterruptedFileSwitch(active, previous string) error {
	previousInfo, err := os.Lstat(previous)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !previousInfo.IsDir() || previousInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("deployment rollback path is not a trusted directory")
	}
	activeInfo, activeErr := os.Lstat(active)
	if activeErr == nil {
		if !activeInfo.IsDir() || activeInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("active site path is not a trusted directory")
		}
		return os.RemoveAll(previous)
	}
	if !os.IsNotExist(activeErr) {
		return activeErr
	}
	if err := os.Rename(previous, active); err != nil {
		return fmt.Errorf("recover interrupted deployment switch: %w", err)
	}
	return nil
}

func (h *Host) exportSelectedTables(ctx context.Context, site model.Site, identity Identity, publicDir string, tables []string, suffix string) (string, error) {
	directory := filepath.Join(filepath.Dir(publicDir), "tmp", ".wpx-db-selected-"+suffix)
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", fmt.Errorf("prepare selected table export: %w", err)
	}
	if err := os.Chown(directory, identity.UID, identity.GID); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "database.sql")
	wpcli := h.WordPress.(*WPCLI)
	arguments := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "db", "export", path, "--tables=" + strings.Join(tables, ","), "--single-transaction", "--quiet"}
	if err := h.Runner.Run(ctx, "/usr/sbin/runuser", arguments...); err != nil {
		return "", fmt.Errorf("export selected staging tables: %w", err)
	}
	return path, nil
}

func (h *Host) replaceURLsInTables(ctx context.Context, staging, production model.Site, identity Identity, publicDir string, tables []string) error {
	wpcli := h.WordPress.(*WPCLI)
	targetScheme := "http://"
	if production.TLSStatus == "active" {
		targetScheme = "https://"
	}
	for _, sourceScheme := range []string{"http://", "https://"} {
		arguments := []string{"--user", identity.Name, "--", "/usr/bin/php" + production.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "search-replace", sourceScheme + staging.Domain, targetScheme + production.Domain}
		arguments = append(arguments, tables...)
		arguments = append(arguments, "--precise", "--skip-columns=guid")
		if err := h.Runner.Run(ctx, "/usr/sbin/runuser", arguments...); err != nil {
			return fmt.Errorf("replace serialized staging URLs in selected tables: %w", err)
		}
	}
	return nil
}

func applySelectedFile(sourceRoot, targetRoot, relative string) error {
	if relative == "" || relative != filepath.ToSlash(filepath.Clean(relative)) || filepath.IsAbs(relative) || strings.HasPrefix(relative, "../") || excludedStagingPath(relative) {
		return errors.New("unsafe selected path")
	}
	source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
	destination := filepath.Join(targetRoot, filepath.FromSlash(relative))
	if err := ensureContained(sourceRoot, source); err != nil {
		return err
	}
	if err := ensureContained(targetRoot, destination); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		if targetInfo, targetErr := os.Lstat(destination); targetErr == nil {
			if targetInfo.IsDir() {
				return errors.New("selected file became a directory")
			}
			return os.Remove(destination)
		} else if os.IsNotExist(targetErr) {
			return nil
		} else {
			return targetErr
		}
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("selected source is not a regular file")
	}
	if err := ensureSafeDirectories(targetRoot, filepath.Dir(filepath.FromSlash(relative))); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".wpx-selected-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

func ensureSafeDirectories(root, relative string) error {
	current := root
	if relative == "." || relative == "" {
		return nil
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0750); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("selected path traverses a non-directory")
		}
	}
	return nil
}

func (h *Host) replaceWordPressEnvironment(ctx context.Context, source, target model.Site, protectStaging bool) error {
	sourceDir := filepath.Join(h.SiteRoot, source.ID)
	targetDir := filepath.Join(h.SiteRoot, target.ID)
	if err := ensureContained(h.SiteRoot, sourceDir); err != nil {
		return err
	}
	if err := ensureContained(h.SiteRoot, targetDir); err != nil {
		return err
	}
	sourcePublic, targetPublic := filepath.Join(sourceDir, "public"), filepath.Join(targetDir, "public")
	targetConfig, err := os.ReadFile(filepath.Join(targetPublic, "wp-config.php"))
	if err != nil {
		return fmt.Errorf("preserve staging database configuration: %w", err)
	}
	sourceIdentity, err := h.Identities.Ensure(ctx, source, sourceDir)
	if err != nil {
		return err
	}
	targetIdentity, err := h.Identities.Ensure(ctx, target, targetDir)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(source.ID+":"+target.ID)))
	databaseRollback, err := h.exportRestoreDatabase(ctx, target, targetIdentity, targetPublic, "staging-rollback-"+digest[:16])
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(databaseRollback))
	databaseExport, err := h.exportRestoreDatabase(ctx, source, sourceIdentity, sourcePublic, "staging-"+digest[:16])
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(databaseExport))
	databaseImport, err := stageDatabaseForSite(targetDir, databaseExport, targetIdentity, "staging-"+digest[:16])
	if err != nil {
		return fmt.Errorf("stage production database for import: %w", err)
	}
	defer os.RemoveAll(filepath.Dir(databaseImport))
	clonePublic, err := os.MkdirTemp(targetDir, ".wpx-staging-build-")
	if err != nil {
		return fmt.Errorf("prepare staging clone: %w", err)
	}
	defer os.RemoveAll(clonePublic)
	if err := h.Runner.Run(ctx, "/usr/bin/cp", "-a", sourcePublic+"/.", clonePublic+"/"); err != nil {
		return fmt.Errorf("copy production files: %w", err)
	}
	configPath := filepath.Join(clonePublic, "wp-config.php")
	if err := atomicWrite(configPath, targetConfig, 0640); err != nil {
		return fmt.Errorf("restore staging database configuration: %w", err)
	}
	for _, path := range magicLoginFiles(clonePublic) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove copied one-time login: %w", err)
		}
	}
	if err := chownTree(clonePublic, targetIdentity); err != nil {
		return fmt.Errorf("own staging files: %w", err)
	}
	rollbackDatabase := func(operationErr error) error {
		if rollbackErr := h.runWPDatabase(ctx, target, targetIdentity, targetPublic, "import", databaseRollback); rollbackErr != nil {
			return fmt.Errorf("%v; staging database rollback also failed: %w", operationErr, rollbackErr)
		}
		return operationErr
	}
	if err := h.runWPDatabase(ctx, target, targetIdentity, clonePublic, "import", databaseImport); err != nil {
		return rollbackDatabase(fmt.Errorf("import production database into staging: %w", err))
	}
	wpcli := h.WordPress.(*WPCLI)
	run := func(arguments ...string) error {
		base := []string{"--user", targetIdentity.Name, "--", "/usr/bin/php" + target.PHPVersion, wpcli.Path, "--path=" + clonePublic, "--no-color"}
		return h.Runner.Run(ctx, "/usr/sbin/runuser", append(base, arguments...)...)
	}
	targetScheme := "http://"
	if target.TLSStatus == "active" {
		targetScheme = "https://"
	}
	for _, scheme := range []string{"http://", "https://"} {
		if err := run("search-replace", scheme+source.Domain, targetScheme+target.Domain, "--all-tables-with-prefix", "--precise", "--skip-columns=guid"); err != nil {
			return rollbackDatabase(fmt.Errorf("replace serialized production URLs: %w", err))
		}
	}
	if err := run("option", "update", "home", targetScheme+target.Domain); err != nil {
		return rollbackDatabase(err)
	}
	if err := run("option", "update", "siteurl", targetScheme+target.Domain); err != nil {
		return rollbackDatabase(err)
	}
	blogPublic := "1"
	if protectStaging {
		blogPublic = "0"
	}
	if err := run("option", "update", "blog_public", blogPublic); err != nil {
		return rollbackDatabase(fmt.Errorf("discourage staging indexing: %w", err))
	}
	if protectStaging {
		if err := installStagingGuard(clonePublic, targetIdentity); err != nil {
			return rollbackDatabase(err)
		}
	} else {
		guard := filepath.Join(clonePublic, "wp-content", "mu-plugins", "wpx-staging.php")
		if err := os.Remove(guard); err != nil && !os.IsNotExist(err) {
			return rollbackDatabase(fmt.Errorf("remove staging safeguards from production: %w", err))
		}
	}
	if err := h.checkWordPressHealth(ctx, target, targetIdentity, clonePublic); err != nil {
		return rollbackDatabase(fmt.Errorf("deployed WordPress health check: %w", err))
	}
	rollbackPublic := filepath.Join(targetDir, ".wpx-environment-previous-"+digest[:16])
	if err := recoverInterruptedFileSwitch(targetPublic, rollbackPublic); err != nil {
		return rollbackDatabase(err)
	}
	if err := os.Rename(targetPublic, rollbackPublic); err != nil {
		return rollbackDatabase(fmt.Errorf("preserve current staging files: %w", err))
	}
	if err := os.Rename(clonePublic, targetPublic); err != nil {
		_ = os.Rename(rollbackPublic, targetPublic)
		return rollbackDatabase(fmt.Errorf("activate cloned staging files: %w", err))
	}
	_ = os.RemoveAll(rollbackPublic)
	return nil
}

func (h *Host) checkWordPressHealth(ctx context.Context, site model.Site, identity Identity, publicDir string) error {
	wpcli, ok := h.WordPress.(*WPCLI)
	if !ok || h.Runner == nil || !filepath.IsAbs(wpcli.Path) {
		return errors.New("WordPress health check requires WP-CLI")
	}
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color"}
	for _, command := range [][]string{{"core", "is-installed"}, {"core", "verify-checksums"}, {"db", "check"}} {
		if err := h.Runner.Run(ctx, "/usr/sbin/runuser", append(base, command...)...); err != nil {
			return fmt.Errorf("WP-CLI %s: %w", strings.Join(command, " "), err)
		}
	}
	return nil
}

func magicLoginFiles(publicDir string) []string {
	paths, _ := filepath.Glob(filepath.Join(publicDir, "wpx-login-*.php"))
	return paths
}

func installStagingGuard(publicDir string, identity Identity) error {
	directory := filepath.Join(publicDir, "wp-content", "mu-plugins")
	if err := ensureDirectory(directory, 0750, identity); err != nil {
		return fmt.Errorf("prepare staging safeguards: %w", err)
	}
	content := []byte("<?php\n/** Managed by WPX: keep staging side effects local. */\nadd_filter('pre_wp_mail', '__return_false', PHP_INT_MAX);\nadd_filter('pre_option_blog_public', static function () { return '0'; });\n")
	path := filepath.Join(directory, "wpx-staging.php")
	if err := atomicWrite(path, content, 0640); err != nil {
		return fmt.Errorf("write staging safeguards: %w", err)
	}
	if err := os.Chown(path, identity.UID, identity.GID); err != nil {
		return fmt.Errorf("own staging safeguards: %w", err)
	}
	return nil
}

func (h *Host) installStagingAuthentication(ctx context.Context, site model.Site, username, password string) error {
	if !filepath.IsAbs(h.StagingAuthRoot) || h.StagingAuthRoot == "/" {
		return errors.New("invalid staging authentication root")
	}
	if err := os.MkdirAll(h.StagingAuthRoot, 0750); err != nil {
		return fmt.Errorf("prepare staging authentication: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return fmt.Errorf("hash staging password: %w", err)
	}
	// Apache-style password files and Ubuntu's crypt implementation use the
	// $2y$ bcrypt marker; the payload is otherwise identical to Go's $2a$ hash.
	encoded := strings.Replace(string(hash), "$2a$", "$2y$", 1)
	path := filepath.Join(h.StagingAuthRoot, site.ID+".htpasswd")
	if err := atomicWrite(path, []byte(username+":"+encoded+"\n"), 0640); err != nil {
		return fmt.Errorf("write staging authentication: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/chown", "root:www-data", path); err != nil {
		return fmt.Errorf("grant nginx staging authentication access: %w", err)
	}
	return nil
}
