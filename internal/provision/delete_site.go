package provision

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

// Deletion needs lookup without creation: Ensure would recreate an account on
// replay after userdel had succeeded. Implementations must refuse an account
// whose name, home, UID or private group no longer belongs to the site.
type siteDeletionIdentities interface {
	InspectSite(context.Context, model.Site, string) (Identity, bool, error)
	StopSite(context.Context, model.Site, string, Identity) error
	RemoveSite(context.Context, model.Site, string, Identity) error
}

type siteDeletionRecord struct {
	SiteID         string         `json:"site_id"`
	Domain         string         `json:"domain"`
	Kind           model.SiteKind `json:"kind"`
	SiteDir        string         `json:"site_dir"`
	Configuration  string         `json:"configuration"`
	Identity       *Identity      `json:"identity,omitempty"`
	Database       string         `json:"database,omitempty"`
	PHPVersions    []string       `json:"php_versions,omitempty"`
	RuntimeStopped bool           `json:"runtime_stopped"`
	Done           bool           `json:"done"`
}

type deletionFile struct {
	path   string
	marker string
}

// DeleteSite is deliberately not a rollback operation. Once traffic is removed,
// deleting the generated database and tree is permanent. The root-owned record
// survives outside that tree so the same durable job can finish after any
// interruption, including success before SQLite records completion.
//
// Certificate lineages, logs, DNS records and remote backup repositories remain:
// their ownership/lifetime is not the lifetime of one site. Application databases
// created outside WPX are likewise never guessed from editable application code.
func (h *Host) DeleteSite(ctx context.Context, site model.Site, _ bool, jobKey string) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if site.Status != "deleting" && site.Status != "delete_failed" {
		return errors.New("site is not awaiting deletion")
	}
	if jobKey == "" || len(jobKey) > 160 || strings.ContainsAny(jobKey, "\r\n") {
		return errors.New("invalid site deletion job key")
	}
	if err := h.validate(); err != nil {
		return err
	}
	identities, ok := h.Identities.(siteDeletionIdentities)
	if !ok {
		return errors.New("site deletion identity manager is unavailable")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := deletionSafePath(siteDir); err != nil {
		return fmt.Errorf("inspect site directory: %w", err)
	}
	journalRoot := filepath.Join(h.DataRoot, "deletions")
	if journalRoot == siteDir || strings.HasPrefix(journalRoot, siteDir+string(filepath.Separator)) {
		return errors.New("site deletion journal must be outside the deleted tree")
	}
	if err := deletionSafePath(journalRoot); err != nil {
		return err
	}
	if err := os.MkdirAll(journalRoot, 0700); err != nil {
		return fmt.Errorf("prepare site deletion journal: %w", err)
	}
	if err := deletionPrivateFile(journalRoot, true); err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	journalPath := filepath.Join(journalRoot, digest+".json")
	configuration, err := h.deletionConfiguration(site)
	if err != nil {
		return err
	}
	record, exists, err := readSiteDeletion(journalPath)
	if err != nil {
		return err
	}
	if exists {
		if record.SiteID != site.ID || record.Domain != site.Domain || record.Kind != site.Kind || record.SiteDir != siteDir || record.Configuration != configuration {
			return errors.New("site deletion job does not match its saved operation")
		}
		if record.Done {
			return nil
		}
		identity, found, err := identities.InspectSite(ctx, site, siteDir)
		if err != nil {
			return err
		}
		if found && (record.Identity == nil || identity != *record.Identity) {
			return errors.New("site identity changed since deletion began")
		}
	} else {
		record = siteDeletionRecord{SiteID: site.ID, Domain: site.Domain, Kind: site.Kind, SiteDir: siteDir, Configuration: configuration}
		identity, found, err := identities.InspectSite(ctx, site, siteDir)
		if err != nil {
			return err
		}
		if found {
			record.Identity = &identity
		}
		if site.Kind == model.WordPress {
			record.Database, err = h.inspectDeletionDatabase(site, siteDir)
			if err != nil {
				return err
			}
		}
	}
	if err := validateDeletionTree(siteDir, record.Identity); err != nil {
		return err
	}
	files, phpVersions, err := h.siteDeletionFiles(site)
	if err != nil {
		return err
	}
	if exists {
		// A previous attempt may already have unlinked a pool on a branch
		// other than the selected version. Keep the pre-deletion list; scanning
		// files again would forget to reload that still-running FPM process.
		phpVersions = record.PHPVersions
		for _, version := range phpVersions {
			if !model.ValidPHPVersion(version) {
				return errors.New("saved deletion PHP branch is invalid")
			}
		}
	} else {
		record.PHPVersions = phpVersions
	}
	// Inspect every generated path before removing the first traffic link. A
	// copied marker is not sufficient for the site tree: its account and exact
	// home directory were independently verified above.
	for _, file := range files {
		if err := inspectDeletionFile(file); err != nil {
			return err
		}
	}
	for _, name := range []string{"wpx-" + site.ID + ".conf", "wpx-" + site.ID + "-tls.conf"} {
		path := filepath.Join(h.NginxEnabled, name)
		if err := deletionSafePath(filepath.Dir(path)); err != nil {
			return err
		}
		target, err := os.Readlink(path)
		if err != nil && !os.IsNotExist(err) || err == nil && target != filepath.Join(h.NginxAvailable, name) {
			return fmt.Errorf("refuse to remove unmanaged Nginx link %s", path)
		}
	}
	if !exists {
		if err := writeSiteDeletion(journalPath, record); err != nil {
			return err
		}
	}

	for _, name := range []string{"wpx-" + site.ID + ".conf", "wpx-" + site.ID + "-tls.conf"} {
		if err := removeDeletionFile(filepath.Join(h.NginxEnabled, name)); err != nil {
			return err
		}
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return fmt.Errorf("validate Nginx before deleting site: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return fmt.Errorf("remove site traffic: %w", err)
	}
	if !record.RuntimeStopped && site.Kind == model.Python {
		base := "wpx-python-" + site.ID
		for _, command := range [][]string{{"disable", "--now", base + ".socket"}, {"stop", base + ".service"}} {
			// Unit files remain until after these calls, including on replay.
			if err := h.Runner.Run(ctx, "/usr/bin/systemctl", command...); err != nil {
				return fmt.Errorf("stop site Python runtime: %w", err)
			}
		}
	}
	// Remove pools before reloading; host files, rather than a stale site count
	// in the worker payload, decide whether a shared PHP branch remains needed.
	if php, ok := h.PHP.(*AptPHPRuntime); ok && !record.RuntimeStopped {
		for _, version := range phpVersions {
			pool := filepath.Join(php.ConfigRoot, version, "fpm", "pool.d", "wpx-"+site.ID+".conf")
			if err := removeDeletionFile(pool); err != nil {
				return err
			}
			if err := php.reloadOrStopUnused(ctx, version); err != nil {
				return fmt.Errorf("release deleted site's PHP pool: %w", err)
			}
		}
		if len(phpVersions) > 0 {
			if err := waitDeletionSocketStopped(ctx, filepath.Join(php.RunRoot, "wpx-"+site.ID+".sock")); err != nil {
				return err
			}
		}
	}
	if !record.RuntimeStopped {
		record.RuntimeStopped = true
		if err := writeSiteDeletion(journalPath, record); err != nil {
			return err
		}
	}
	if record.Identity != nil {
		if err := identities.StopSite(ctx, site, siteDir, *record.Identity); err != nil {
			return fmt.Errorf("stop remaining site processes: %w", err)
		}
	}
	if record.Database != "" {
		manager, ok := h.Database.(*MariaDB)
		if !ok || manager.SQL == nil {
			return errors.New("site database deletion manager is unavailable")
		}
		if record.Database != deletionDatabaseName(site.ID) {
			return errors.New("saved deletion database is not owned by this site")
		}
		statement := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\nDROP USER IF EXISTS '%s'@'localhost';\n", record.Database, record.Database)
		if err := manager.SQL.Execute(ctx, statement); err != nil {
			return fmt.Errorf("delete generated WordPress database: %w", err)
		}
		if err := removeDeletionFile(filepath.Join(manager.SecretsRoot, site.ID+".json")); err != nil {
			return err
		}
	}
	// OpenRoot keeps recursive removal confined to this exact child even if a
	// symlink appears inside the application tree. Mount points are rejected by
	// validateDeletionTree; they are not a licence to delete another filesystem.
	if _, err := os.Lstat(siteDir); err == nil {
		root, err := os.OpenRoot(h.SiteRoot)
		if err != nil {
			return err
		}
		err = root.RemoveAll(site.ID)
		root.Close()
		if err != nil {
			return fmt.Errorf("remove site files: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, file := range files {
		if err := removeDeletionFile(file.path); err != nil {
			return err
		}
	}
	if site.Kind == model.Python {
		if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			return fmt.Errorf("reload systemd after deleting site: %w", err)
		}
	}
	if record.Identity != nil {
		// Reserve the UID until its files and database are gone. Releasing it
		// earlier would let a subsequently created site inherit access to old
		// private files if this deletion failed between userdel and RemoveAll.
		if err := identities.RemoveSite(ctx, site, siteDir, *record.Identity); err != nil {
			return fmt.Errorf("remove deleted site's account: %w", err)
		}
	}
	record.Done = true
	return writeSiteDeletion(journalPath, record)
}

func (h *Host) deletionConfiguration(site model.Site) (string, error) {
	paths := []string{h.SiteRoot, h.NginxAvailable, h.NginxEnabled, h.NginxSnippetRoot, h.PHPSnippetRoot}
	if site.Environment == "staging" {
		paths = append(paths, h.StagingAuthRoot)
	}
	if site.Kind == model.PHP || site.Kind == model.WordPress {
		php, ok := h.PHP.(*AptPHPRuntime)
		if !ok || php.Runner == nil {
			return "", errors.New("site deletion PHP manager is unavailable")
		}
		paths = append(paths, php.ConfigRoot, php.RunRoot)
	}
	if site.Kind == model.Python {
		python, ok := h.Python.(*SystemPython)
		if !ok || python.Runner == nil {
			return "", errors.New("site deletion Python manager is unavailable")
		}
		paths = append(paths, python.UnitRoot)
	}
	if site.Kind == model.WordPress {
		database, ok := h.Database.(*MariaDB)
		if !ok || database.SQL == nil {
			return "", errors.New("site deletion database manager is unavailable")
		}
		paths = append(paths, database.SecretsRoot)
	}
	for _, path := range paths {
		if err := deletionSafePath(path); err != nil {
			return "", err
		}
	}
	// Bind retry to the same configured resource roots. Moving a configuration
	// directory while an irreversible job is pending requires operator recovery,
	// not guessing that an identically named resource at a new path is the same.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(paths, "\x00")))), nil
}

func (h *Host) siteDeletionFiles(site model.Site) ([]deletionFile, []string, error) {
	files := []deletionFile{
		{filepath.Join(h.NginxAvailable, "wpx-"+site.ID+".conf"), ownershipMarker},
		{filepath.Join(h.NginxAvailable, "wpx-"+site.ID+"-tls.conf"), ownershipMarker},
		{filepath.Join(h.NginxSnippetRoot, site.ID+".conf"), ownershipMarker},
		{filepath.Join(h.PHPSnippetRoot, site.ID+".conf"), phpOwnershipMarker},
	}
	if site.Environment == "staging" {
		// The auth file has no comment syntax; its private directory and exact
		// site-derived name are the ownership boundary.
		files = append(files, deletionFile{filepath.Join(h.StagingAuthRoot, site.ID+".htpasswd"), ""})
	}
	var versions []string
	if site.Kind == model.PHP || site.Kind == model.WordPress {
		php, ok := h.PHP.(*AptPHPRuntime)
		if !ok || php.Runner == nil {
			return nil, nil, errors.New("site deletion PHP manager is unavailable")
		}
		if err := deletionSafePath(php.ConfigRoot); err != nil {
			return nil, nil, err
		}
		entries, err := os.ReadDir(php.ConfigRoot)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		for _, entry := range entries {
			if !model.ValidPHPVersion(entry.Name()) {
				continue
			}
			pool := filepath.Join(php.ConfigRoot, entry.Name(), "fpm", "pool.d", "wpx-"+site.ID+".conf")
			files = append(files, deletionFile{pool, phpOwnershipMarker}, deletionFile{pool + ".wpx-previous", phpOwnershipMarker})
			// Replay still reconciles the selected runtime after a crash between
			// removing its pool and reloading the branch.
			if _, err := os.Lstat(pool); err == nil || entry.Name() == site.PHPVersion {
				versions = append(versions, entry.Name())
			}
		}
	}
	if site.Kind == model.Python {
		python, ok := h.Python.(*SystemPython)
		if !ok || python.Runner == nil {
			return nil, nil, errors.New("site deletion Python manager is unavailable")
		}
		files = append(files, deletionFile{filepath.Join(python.UnitRoot, "wpx-python-"+site.ID+".socket"), ownershipMarker}, deletionFile{filepath.Join(python.UnitRoot, "wpx-python-"+site.ID+".service"), ownershipMarker})
	}
	return files, versions, nil
}

func deletionDatabaseName(siteID string) string {
	digest := sha256.Sum256([]byte(siteID))
	return fmt.Sprintf("wpx_%x", digest[:8])
}

func (h *Host) inspectDeletionDatabase(site model.Site, siteDir string) (string, error) {
	m, ok := h.Database.(*MariaDB)
	if !ok || m.SQL == nil {
		return "", errors.New("site database deletion manager is unavailable")
	}
	path := filepath.Join(m.SecretsRoot, site.ID+".json")
	if err := deletionSafePath(path); err != nil {
		return "", err
	}
	if err := deletionPrivateFile(path, false); os.IsNotExist(err) {
		if _, configErr := os.Lstat(filepath.Join(siteDir, "public", "wp-config.php")); !os.IsNotExist(configErr) {
			return "", errors.New("WordPress database ownership record is missing; refusing deletion")
		}
		return "", nil // Provisioning may have failed before creating a database.
	} else if err != nil {
		return "", err
	}
	credentials, err := loadDatabaseCredentials(path)
	if err != nil {
		return "", err
	}
	expected := deletionDatabaseName(site.ID)
	if credentials.Name != expected || credentials.User != expected || credentials.Host != "localhost" {
		return "", errors.New("stored WordPress database is not generated for this site")
	}
	return expected, nil
}

// Every existing component is inspected with Lstat, not Stat. Only enabled
// Nginx links are allowed symlinks, and those are verified separately. Checking
// ancestors also prevents a hostile intermediate directory from redirecting a
// trusted, site-derived filename outside its configured root.
func deletionSafePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return errors.New("deletion path must be absolute, clean and non-root")
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink in deletion path %s", current)
		}
	}
	return nil
}

func deletionPrivateFile(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 || info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return fmt.Errorf("deletion ownership record is not a private root-owned file: %s", path)
	}
	return nil
}

func inspectDeletionFile(file deletionFile) error {
	if err := deletionSafePath(file.path); err != nil {
		return err
	}
	info, err := os.Lstat(file.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("refuse unmanaged deletion file %s", file.path)
	}
	if file.marker != "" {
		if _, _, err := managedFileStateWithMarker(file.path, file.marker); err != nil {
			return fmt.Errorf("inspect deletion file %s: %w", file.path, err)
		}
	}
	return nil
}

func validateDeletionTree(path string, identity *Identity) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if identity == nil || !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(stat.Uid) != identity.UID || int(stat.Gid) != identity.GID {
		return errors.New("refuse site tree without its verified dedicated account ownership")
	}
	if runtime.GOOS == "linux" {
		mounts, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return fmt.Errorf("inspect mounted filesystems before deletion: %w", err)
		}
		if err := validateDeletionMounts(path, string(mounts)); err != nil {
			return err
		}
	}
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil // RemoveAll unlinks nested symlinks; it never follows them.
		}
		child, err := entry.Info()
		if err != nil {
			return err
		}
		childStat, ok := child.Sys().(*syscall.Stat_t)
		if !ok || childStat.Dev != stat.Dev {
			return fmt.Errorf("refuse mounted filesystem inside site tree: %s", current)
		}
		return nil
	})
}

func validateDeletionMounts(path, mountinfo string) error {
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mount := unescape.Replace(fields[4])
		if mount == path || strings.HasPrefix(mount, path+string(filepath.Separator)) {
			return fmt.Errorf("refuse mounted filesystem inside site tree: %s", mount)
		}
	}
	return nil
}

func readSiteDeletion(path string) (siteDeletionRecord, bool, error) {
	var record siteDeletionRecord
	if err := deletionSafePath(path); err != nil {
		return record, false, err
	}
	if err := deletionPrivateFile(path, false); os.IsNotExist(err) {
		return record, false, nil
	} else if err != nil {
		return record, false, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return record, false, err
	}
	if err := json.Unmarshal(content, &record); err != nil {
		return record, false, fmt.Errorf("read deletion journal: %w", err)
	}
	return record, true, nil
}

func writeSiteDeletion(path string, record siteDeletionRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, append(encoded, '\n'), 0600); err != nil {
		return fmt.Errorf("save site deletion journal: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeDeletionFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove site resource %s: %w", path, err)
	}
	return nil
}

func waitDeletionSocketStopped(ctx context.Context, path string) error {
	// systemctl reload may acknowledge its signal before FPM finishes applying
	// configuration. Keep the account reserved until its old listener is gone,
	// then stop remaining workers before deleting files or freeing the UID.
	if err := deletionSafePath(path); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	for {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("deleted site's PHP listener path is not a Unix socket")
		}
		connection, err := dialer.DialContext(waitCtx, "unix", path)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil
		}
		if err == nil {
			connection.Close()
		} else if waitCtx.Err() == nil {
			return fmt.Errorf("inspect deleted site's PHP listener: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("deleted site's PHP listener did not stop: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
