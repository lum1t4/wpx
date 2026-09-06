package provision

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

// streamOutputRunner keeps large SQL exports out of memory and command logs.
// The root process owns the destination; runuser never receives its path or
// permission to replace the recovery copy.
type streamOutputRunner interface {
	RunOutput(context.Context, io.Writer, string, ...string) error
}

func (r ExecRunner) RunOutput(ctx context.Context, output io.Writer, executable string, args ...string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdout = output
	cmd.Stderr = r.Output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run %s with output: %w", filepath.Base(executable), err)
	}
	return nil
}

type domainVhostState struct {
	Content []byte `json:"content"`
	Exists  bool   `json:"exists"`
	Enabled bool   `json:"enabled"`
}

type domainChangeJournal struct {
	Version         int                `json:"version"`
	SiteID          string             `json:"site_id"`
	JobKey          string             `json:"job_key"`
	Change          model.DomainChange `json:"change"`
	HTTP            domainVhostState   `json:"http"`
	TLS             domainVhostState   `json:"tls"`
	BackupReady     bool               `json:"backup_ready"`
	DatabaseStarted bool               `json:"database_started"`
	State           string             `json:"state"`
}

// ChangeDomain preserves the site identity, paths, runtime and database names.
// A per-job journal is written before changing traffic, and a completion marker
// before reporting success. Replaying after a lost response does not repeat a
// successful change or overwrite its original recovery database.
//
// The site's Nginx vhosts return 503 while its WordPress database is changed.
// This does not freeze external cron jobs or already-running requests: callers
// must schedule write-heavy sites appropriately. Recovery is an operation-local
// database snapshot, not a substitute for an off-server backup.
func (h *Host) ChangeDomain(ctx context.Context, site model.Site, change model.DomainChange, jobKey string) error {
	if err := model.ValidateDomainChange(site, change); err != nil {
		return err
	}
	if site.Status != "domain_changing" {
		return errors.New("site is not awaiting a domain change")
	}
	if jobKey == "" || len(jobKey) > 160 || strings.ContainsAny(jobKey, "\x00\r\n") {
		return errors.New("invalid domain-change job key")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()

	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	for _, path := range []string{h.SiteRoot, siteDir, publicDir, h.NginxAvailable, h.NginxEnabled, h.DataRoot} {
		if err := domainRealDirectory(path); err != nil {
			return err
		}
	}
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return err
	}
	recovery, err := h.openDomainRecovery(site.ID, jobKey)
	if err != nil {
		return err
	}
	defer recovery.root.Close()
	journalDir := recovery.directory
	journal, exists, err := recovery.read()
	if err != nil {
		return err
	}
	if exists && (journal.Version != 1 || journal.SiteID != site.ID || journal.JobKey != jobKey || journal.Change != change) {
		return errors.New("domain recovery journal belongs to another operation")
	}
	if exists && journal.State == "complete" {
		return nil
	}
	if exists && journal.State == "restored" {
		return &model.DomainChangeError{Err: errors.New("previous domain restored; create a new change to retry"), PreviousRestored: true}
	}
	if exists && journal.State == "activating" {
		return h.finishDomainActivation(ctx, site, publicDir, recovery, &journal)
	}
	if exists && journal.State != "changing" && journal.State != "rolling_back" && journal.State != "restoring_traffic" {
		return errors.New("domain recovery journal has an unknown state")
	}

	var identity Identity
	var wpBase []string
	if site.Kind == model.WordPress {
		wp, ok := h.WordPress.(*WPCLI)
		if !ok || !filepath.IsAbs(wp.Path) || h.Input == nil {
			return errors.New("WordPress domain changes require WP-CLI and database streaming")
		}
		if _, ok := h.Runner.(streamOutputRunner); !ok {
			return errors.New("WordPress database export streaming is unavailable")
		}
		identity, err = h.Identities.Ensure(ctx, site, siteDir)
		if err != nil {
			return err
		}
		wpBase = []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wp.Path, "--path=" + publicDir, "--no-color", "--skip-plugins", "--skip-themes"}
		// The on-disk installation may have been converted outside WPX. Refuse
		// multisite and config-constant URLs rather than partially rewriting a
		// network or claiming that DB option changes overrode wp-config.php.
		preflight := "if (is_multisite() || defined('WP_HOME') || defined('WP_SITEURL')) { WP_CLI::error('Domain changes require single-site WordPress without WP_HOME/WP_SITEURL constants.'); }" +
			" foreach (array('home', 'siteurl') as $key) { if (!in_array(get_option($key), array('http://" + site.Domain + "', 'https://" + site.Domain + "', 'http://" + change.Domain + "'), true)) { WP_CLI::error('WordPress URLs do not match this root-domain installation.'); } }" + domainWordPressCacheGuard(site.ID)
		if err := func() error {
			if exists && journal.State != "changing" {
				return nil // A partial import may not boot WordPress yet.
			}
			return h.Runner.Run(ctx, "/usr/sbin/runuser", append(wpBase, "eval", preflight)...)
		}(); err != nil {
			if !exists {
				return &model.DomainChangeError{Err: fmt.Errorf("WordPress domain preflight: %w", err), PreviousRestored: true}
			}
			return fmt.Errorf("resume WordPress domain change: %w; recovery at %s", err, journalDir)
		}
	}
	if exists && (journal.State == "rolling_back" || journal.State == "restoring_traffic") {
		return h.rollbackDomainChange(site, wpBase, recovery, &journal, errors.New("resume interrupted domain recovery"))
	}
	if !exists {
		http, err := h.inspectDomainVhost(site.ID, false)
		if err != nil {
			return err
		}
		tls, err := h.inspectDomainVhost(site.ID, true)
		if err != nil {
			return err
		}
		if !http.Exists || !http.Enabled || !strings.Contains(string(http.Content), "    server_name "+site.Domain+";") {
			return errors.New("current domain does not match the active managed HTTP virtual host")
		}
		if site.TLSStatus == "active" && (!tls.Exists || !tls.Enabled) {
			return errors.New("current TLS virtual host is missing")
		}
		if tls.Enabled && (!strings.Contains(string(tls.Content), "    server_name "+site.Domain+";") || strings.Count(string(tls.Content), "server {\n") != 1) {
			return errors.New("current TLS virtual host cannot be safely paused for this domain")
		}
		journal = domainChangeJournal{Version: 1, SiteID: site.ID, JobKey: jobKey, Change: change, HTTP: http, TLS: tls, State: "changing"}
		if err := recovery.write(journal); err != nil {
			return err
		}
	}
	rollback := func(cause error) error {
		return h.rollbackDomainChange(site, wpBase, recovery, &journal, cause)
	}
	// The temporary vhost also claims the new HTTP name, so it cannot fall
	// through to an unrelated default site while its database is being changed.
	gate := ownershipMarker + "server {\n    listen 80;\n    listen [::]:80;\n    server_name " + site.Domain + " " + change.Domain + ";\n    add_header Retry-After 60 always;\n    return 503;\n}\n"
	if err := h.writeDomainVhosts(site.ID, domainVhostState{Content: []byte(gate), Exists: true, Enabled: true}, domainMaintenanceTLS(journal.TLS)); err != nil {
		return rollback(err)
	}
	if err := h.reloadDomainNginx(ctx); err != nil {
		return rollback(fmt.Errorf("pause domain traffic: %w", err))
	}
	if site.Kind == model.WordPress {
		if !journal.BackupReady {
			if err := h.exportDomainDatabase(ctx, wpBase, recovery); err != nil {
				return rollback(err)
			}
			journal.BackupReady = true
			if err := recovery.write(journal); err != nil {
				return rollback(err)
			}
		}
		journal.DatabaseStarted = true
		if err := recovery.write(journal); err != nil {
			return rollback(err)
		}
		// Match the hostname boundary, not a textual prefix: example.com must
		// never rewrite example.com.au. One serialized-safe regex also makes
		// replay converge when changing to a subdomain of the previous name.
		pattern := `https?://` + regexp.QuoteMeta(site.Domain) + `(?=[:/?#]|$)`
		commands := [][]string{
			{"search-replace", pattern, "http://" + change.Domain, "--regex", "--all-tables-with-prefix", "--precise", "--skip-columns=guid", "--quiet"},
			// Search-replace updates SQL directly. Invalidate cached options
			// before update_option reads them: otherwise Redis may report the
			// old URL and an identical SQL update returns zero affected rows.
			{"eval", domainClearSiteCache(site.ID)},
			{"option", "update", "home", "http://" + change.Domain},
			{"option", "update", "siteurl", "http://" + change.Domain},
			{"eval", domainClearSiteCache(site.ID)},
			{"db", "check"},
		}
		for _, command := range commands {
			if err := h.Runner.Run(ctx, "/usr/sbin/runuser", append(wpBase, command...)...); err != nil {
				return rollback(fmt.Errorf("update WordPress domain (%s): %w", command[0], err))
			}
		}
	}
	// From this durable boundary onward, only finish activation. A reload can
	// accept new writes before the broker replies; restoring the original SQL
	// snapshot after that point would silently discard those writes.
	journal.State = "activating"
	if err := recovery.write(journal); err != nil {
		return fmt.Errorf("save domain activation boundary: %w; resume job using %s", err, journalDir)
	}
	return h.finishDomainActivation(ctx, site, publicDir, recovery, &journal)
}

func (h *Host) finishDomainActivation(ctx context.Context, site model.Site, publicDir string, recovery *domainRecovery, journal *domainChangeJournal) error {
	journalDir := recovery.directory
	target := site
	target.Domain, target.TLSStatus = journal.Change.Domain, "none"
	generated, err := h.renderSiteConfig(target, publicDir)
	if err != nil {
		return fmt.Errorf("render new domain: %w; resume activation using %s", err, journalDir)
	}
	// Remove both the old TLS activation and its available generated vhost;
	// retain certificate files themselves for explicit operator recovery.
	if err := h.writeDomainVhosts(site.ID, domainVhostState{Content: []byte(generated), Exists: true, Enabled: true}, domainVhostState{}); err != nil {
		return fmt.Errorf("write new domain: %w; resume activation using %s", err, journalDir)
	}
	if err := h.reloadDomainNginx(ctx); err != nil {
		return fmt.Errorf("activate new domain: %w; resume activation using %s", err, journalDir)
	}
	journal.State = "complete"
	if err := recovery.write(*journal); err != nil {
		return fmt.Errorf("new domain active but completion marker failed: %w; resume job using %s", err, journalDir)
	}
	return nil
}

func domainMaintenanceTLS(previous domainVhostState) domainVhostState {
	if !previous.Enabled {
		return previous
	}
	// Preserve the original hostname and certificate directives, but insert a
	// server-level return before any application locations can serve requests.
	previous.Content = []byte(strings.Replace(string(previous.Content), "server {\n", "server {\n    add_header Retry-After 60 always;\n    return 503;\n", 1))
	return previous
}

func (h *Host) rollbackDomainChange(site model.Site, wpBase []string, recovery *domainRecovery, journal *domainChangeJournal, cause error) error {
	journalDir := recovery.directory
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if journal.State == "changing" {
		journal.State = "rolling_back"
		if err := recovery.write(*journal); err != nil {
			return fmt.Errorf("%w; could not record recovery intent: %v; recover from %s", cause, err, journalDir)
		}
	}
	if journal.State != "rolling_back" && journal.State != "restoring_traffic" {
		return fmt.Errorf("%w; recovery refused after domain activation boundary; resume using %s", cause, journalDir)
	}
	if journal.State == "rolling_back" && journal.DatabaseStarted {
		file, err := recovery.openPrivate("database.sql")
		if err != nil {
			return fmt.Errorf("%w; domain database recovery unavailable: %v; recover from %s", cause, err, journalDir)
		}
		if info, err := file.Stat(); err != nil || info.Mode().Perm() != 0600 {
			file.Close()
			return fmt.Errorf("%w; domain recovery database is not private; recover from %s", cause, journalDir)
		}
		err = h.Input.RunInput(ctx, file, "/usr/sbin/runuser", append(wpBase, "db", "import", "-", "--quiet")...)
		file.Close()
		if err == nil {
			err = h.Runner.Run(ctx, "/usr/sbin/runuser", append(wpBase, "eval", domainClearSiteCache(site.ID))...)
		}
		if err != nil {
			return fmt.Errorf("%w; domain database rollback failed: %v; traffic stays paused, recover from %s", cause, err, journalDir)
		}
	}
	if journal.State == "rolling_back" {
		// A successful old-domain reload can accept writes too. Record that SQL
		// recovery finished before opening traffic, so replay cannot import the
		// snapshot again after the old domain has resumed serving visitors.
		journal.State = "restoring_traffic"
		if err := recovery.write(*journal); err != nil {
			return fmt.Errorf("%w; could not record database recovery: %v; recover from %s", cause, err, journalDir)
		}
	}
	if err := h.writeDomainVhosts(site.ID, journal.HTTP, journal.TLS); err != nil {
		return fmt.Errorf("%w; domain configuration rollback failed: %v; recover from %s", cause, err, journalDir)
	}
	if err := h.reloadDomainNginx(ctx); err != nil {
		return fmt.Errorf("%w; previous domain configuration restored but Nginx reload failed: %v; recover from %s", cause, err, journalDir)
	}
	journal.State = "restored"
	if err := recovery.write(*journal); err != nil {
		return fmt.Errorf("%w; previous domain restored but recovery marker failed: %v; recover from %s", cause, err, journalDir)
	}
	return &model.DomainChangeError{Err: fmt.Errorf("%w; previous domain restored", cause), PreviousRestored: true}
}

func (h *Host) reloadDomainNginx(ctx context.Context) error {
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return err
	}
	return h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service")
}

func domainRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("domain change requires a real directory at %s", path)
	}
	return nil
}

func (h *Host) domainJournalDirectory(siteID, jobKey string) (string, error) {
	recovery, err := h.openDomainRecovery(siteID, jobKey)
	if err != nil {
		return "", err
	}
	defer recovery.root.Close()
	return recovery.directory, nil
}

type domainRecovery struct {
	root      *os.Root
	directory string
}

func (h *Host) openDomainRecovery(siteID, jobKey string) (*domainRecovery, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	directory := h.DataRoot
	root, err := os.OpenRoot(h.DataRoot)
	if err != nil {
		return nil, err
	}
	// DataRoot belongs to the unprivileged web account. Pin each private child
	// directory by handle, and perform every journal operation relative to that
	// handle. Renaming its parent cannot redirect root's SQL or journal writes.
	for _, component := range []string{"domain-changes", siteID, digest} {
		directory = filepath.Join(directory, component)
		if err := root.Mkdir(component, 0700); err != nil && !os.IsExist(err) {
			root.Close()
			return nil, err
		}
		info, err := root.Lstat(component)
		if err != nil || !info.IsDir() || !domainOwnedByBroker(info) || info.Mode().Perm() != 0700 {
			root.Close()
			return nil, errors.New("domain recovery directory must already be real, private and broker-owned")
		}
		child, err := root.OpenRoot(component)
		root.Close()
		if err != nil {
			return nil, err
		}
		pinned, err := child.Stat(".")
		if err != nil || !os.SameFile(info, pinned) {
			child.Close()
			return nil, errors.New("domain recovery directory changed while opening")
		}
		root = child
	}
	return &domainRecovery{root: root, directory: directory}, nil
}

func (r *domainRecovery) openPrivate(name string) (*os.File, error) {
	file, err := r.root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !domainOwnedByBroker(info) || info.Mode().Perm() != 0600 {
		file.Close()
		return nil, errors.New("domain recovery file must be private and broker-owned")
	}
	return file, nil
}

func (r *domainRecovery) read() (domainChangeJournal, bool, error) {
	file, err := r.openPrivate("state.json")
	if os.IsNotExist(err) {
		return domainChangeJournal{}, false, nil
	}
	if err != nil {
		return domainChangeJournal{}, false, err
	}
	defer file.Close()
	var journal domainChangeJournal
	if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&journal); err != nil {
		return journal, true, fmt.Errorf("read domain recovery journal: %w", err)
	}
	return journal, true, nil
}

func (r *domainRecovery) temporary(prefix string) (*os.File, string, error) {
	name, err := randomPassword()
	if err != nil {
		return nil, "", err
	}
	name = prefix + name
	file, err := r.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	return file, name, err
}

func (r *domainRecovery) sync() error {
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (r *domainRecovery) write(journal domainChangeJournal) error {
	content, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, name, err := r.temporary(".state-")
	if err != nil {
		return err
	}
	defer r.root.Remove(name)
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := r.root.Rename(name, "state.json"); err != nil {
		return err
	}
	return r.sync()
}

func domainReadFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !domainOwnedByBroker(info) || info.Mode().Perm()&0022 != 0 {
		file.Close()
		return nil, errors.New("domain recovery or configuration path must be a broker-owned regular file without group/other write access")
	}
	return file, nil
}

func domainOwnedByBroker(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	// The production broker requires root; using its effective UID also lets
	// unprivileged tests create fixtures without weakening that host boundary.
	return ok && stat.Uid == uint32(os.Geteuid())
}

func readDomainJournal(path string) (domainChangeJournal, bool, error) {
	file, err := domainReadFile(path)
	if os.IsNotExist(err) {
		return domainChangeJournal{}, false, nil
	}
	if err != nil {
		return domainChangeJournal{}, false, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || info.Mode().Perm() != 0600 {
		return domainChangeJournal{}, true, errors.New("domain recovery journal must be private")
	}
	var journal domainChangeJournal
	if err := json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&journal); err != nil {
		return journal, true, fmt.Errorf("read domain recovery journal: %w", err)
	}
	return journal, true, nil
}

func writeDomainJournal(path string, journal domainChangeJournal) error {
	content, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return domainDurableWrite(path, content, 0600)
}

// Syncing the parent makes rename-based journal boundaries survive a host
// restart as well as a broker restart on filesystems that honor fsync.
func domainDurableWrite(path string, content []byte, mode os.FileMode) error {
	if err := atomicWrite(path, content, mode); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (h *Host) inspectDomainVhost(siteID string, tls bool) (domainVhostState, error) {
	name := "wpx-" + siteID
	if tls {
		name += "-tls"
	}
	name += ".conf"
	available, enabled := filepath.Join(h.NginxAvailable, name), filepath.Join(h.NginxEnabled, name)
	file, err := domainReadFile(available)
	var state domainVhostState
	if err == nil {
		defer file.Close()
		state.Content, err = io.ReadAll(io.LimitReader(file, 1<<20+1))
		if err != nil || len(state.Content) > 1<<20 || !strings.HasPrefix(string(state.Content), ownershipMarker) {
			return state, errors.New("refuse unmanaged or oversized domain virtual host")
		}
		state.Exists = true
	} else if !os.IsNotExist(err) {
		return state, err
	}
	target, err := os.Readlink(enabled)
	if err == nil {
		if target != available || !state.Exists {
			return state, errors.New("enabled domain virtual host points to an unmanaged target")
		}
		state.Enabled = true
	} else if !os.IsNotExist(err) {
		return state, errors.New("enabled domain virtual host is not a managed symlink")
	}
	return state, nil
}

func (h *Host) writeDomainVhosts(siteID string, http, tls domainVhostState) error {
	// Inspect both before changing either: an unmanaged second path must not
	// leave the first path half-updated.
	for _, secure := range []bool{false, true} {
		if _, err := h.inspectDomainVhost(siteID, secure); err != nil {
			return err
		}
	}
	for index, state := range []domainVhostState{http, tls} {
		name := "wpx-" + siteID
		if index == 1 {
			name += "-tls"
		}
		name += ".conf"
		available, enabled := filepath.Join(h.NginxAvailable, name), filepath.Join(h.NginxEnabled, name)
		if state.Exists {
			if err := domainDurableWrite(available, state.Content, 0644); err != nil {
				return err
			}
		}
		if state.Enabled {
			if _, err := os.Readlink(enabled); os.IsNotExist(err) {
				if err := os.Symlink(available, enabled); err != nil {
					return err
				}
			}
		} else if err := os.Remove(enabled); err != nil && !os.IsNotExist(err) {
			return err
		}
		if !state.Exists {
			if err := os.Remove(available); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (h *Host) exportDomainDatabase(ctx context.Context, wpBase []string, recovery *domainRecovery) error {
	file, name, err := recovery.temporary(".database-")
	if err != nil {
		return err
	}
	defer recovery.root.Remove(name)
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	arguments := append(wpBase, "db", "export", "-", "--single-transaction", "--add-drop-table", "--quiet")
	if err := h.Runner.(streamOutputRunner).RunOutput(ctx, file, "/usr/sbin/runuser", arguments...); err != nil {
		return fmt.Errorf("export domain recovery database: %w", err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return errors.New("domain recovery database export is empty")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := recovery.root.Rename(name, "database.sql"); err != nil {
		return err
	}
	return recovery.sync()
}
