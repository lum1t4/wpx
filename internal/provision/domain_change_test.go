package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type domainRunner struct {
	calls      []string
	fail       string
	failed     bool
	exports    int
	imports    int
	failImport bool
	observe    func(string)
}

func (r *domainRunner) Run(_ context.Context, executable string, args ...string) error {
	command := executable + " " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	if r.observe != nil {
		r.observe(command)
	}
	if r.fail != "" && strings.Contains(command, r.fail) && !r.failed {
		r.failed = true
		return errors.New("injected domain failure")
	}
	return nil
}

func (r *domainRunner) RunOutput(ctx context.Context, output io.Writer, executable string, args ...string) error {
	r.exports++
	if err := r.Run(ctx, executable, args...); err != nil {
		return err
	}
	_, err := io.WriteString(output, "-- Original WordPress database\nSELECT 'https://example.com';\n")
	return err
}

func (r *domainRunner) RunInput(ctx context.Context, input io.Reader, executable string, args ...string) error {
	r.imports++
	content, err := io.ReadAll(input)
	if err != nil || !strings.Contains(string(content), "Original WordPress database") {
		return errors.New("invalid SQL recovery stream")
	}
	if r.failImport {
		return errors.New("injected SQL rollback failure")
	}
	return r.Run(ctx, executable, args...)
}

func domainFixture(t *testing.T, kind model.SiteKind, secure bool) (*Host, *domainRunner, model.Site, model.DomainChange) {
	t.Helper()
	runner := &domainRunner{}
	host := testHost(t, runner)
	host.Input = runner
	host.WordPress = &WPCLI{Runner: runner, Path: "/usr/local/lib/wpx/wp-cli.phar"}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: kind, Status: "domain_changing", TLSStatus: "none", Environment: "production"}
	if kind == model.WordPress || kind == model.PHP {
		site.PHPVersion = "8.4"
	}
	if kind == model.ReverseProxy {
		site.Upstream = "http://127.0.0.1:8000"
	}
	if secure {
		site.TLSStatus = "active"
	}
	for _, directory := range []string{filepath.Join(host.SiteRoot, site.ID, "public"), host.NginxAvailable, host.NginxEnabled, host.DataRoot} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	config, err := host.renderSiteConfig(site, filepath.Join(host.SiteRoot, site.ID, "public"))
	if err != nil {
		t.Fatal(err)
	}
	if err := host.activateNginx(context.Background(), site.ID, []byte(config)); err != nil {
		t.Fatal(err)
	}
	if secure {
		tls, err := tlsify(config, site.Domain, host.CertificateRoot)
		if err != nil {
			t.Fatal(err)
		}
		if err := host.activateNginxConfig(context.Background(), "wpx-"+site.ID+"-tls.conf", []byte(tls)); err != nil {
			t.Fatal(err)
		}
	}
	runner.calls = nil
	return host, runner, site, model.DomainChange{PreviousDomain: site.Domain, Domain: "new.example.com", PreviousTLSStatus: site.TLSStatus}
}

func TestDomainChangePreservesIdentityAndNeighborForEverySiteKind(t *testing.T) {
	for _, kind := range []model.SiteKind{model.WordPress, model.PHP, model.Python, model.Static, model.ReverseProxy} {
		t.Run(string(kind), func(t *testing.T) {
			host, runner, site, change := domainFixture(t, kind, true)
			neighbor := filepath.Join(host.NginxAvailable, "wpx-neighbor.conf")
			if err := os.WriteFile(neighbor, []byte("unmanaged neighbor deliberately preserved"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := host.ChangeDomain(context.Background(), site, change, "job-one"); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"))
			if err != nil || !strings.Contains(string(content), "server_name new.example.com;") || !strings.Contains(string(content), site.ID) {
				t.Fatalf("new domain lost site identity: %s, %v", content, err)
			}
			for _, root := range []string{host.NginxAvailable, host.NginxEnabled} {
				if _, err := os.Lstat(filepath.Join(root, "wpx-"+site.ID+"-tls.conf")); !os.IsNotExist(err) {
					t.Fatalf("old certificate virtual host retained at %s: %v", root, err)
				}
			}
			content, _ = os.ReadFile(neighbor)
			if string(content) != "unmanaged neighbor deliberately preserved" {
				t.Fatal("neighbor changed")
			}
			before := len(runner.calls)
			if err := host.ChangeDomain(context.Background(), site, change, "job-one"); err != nil {
				t.Fatalf("completed job replay: %v", err)
			}
			if len(runner.calls) != before {
				t.Fatal("completed job repeated host mutations")
			}
			for _, command := range runner.calls {
				if strings.Contains(command, "certbot") || strings.Contains(command, "useradd") || strings.Contains(command, "apt-get") || strings.Contains(command, "cache flush") {
					t.Fatalf("domain change performed unrelated operation: %s", command)
				}
			}
		})
	}
}

func TestDomainWordPressExportPrecedesSerializedChangesAndKeepsRecoveryPrivate(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.WordPress, true)
	runner.observe = func(command string) {
		if !strings.Contains(command, "search-replace") {
			return
		}
		config, _ := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"))
		if runner.exports != 1 || !strings.Contains(string(config), "return 503;") {
			t.Fatal("database changed before export and maintenance traffic gate")
		}
	}
	if err := host.ChangeDomain(context.Background(), site, change, "wp-change"); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.calls, "\n")
	for _, expected := range []string{`search-replace https?://example\.com(?=[:/?#]|$) http://new.example.com --regex --all-tables-with-prefix --precise --skip-columns=guid`, "option update home http://new.example.com", "option update siteurl http://new.example.com", "is_multisite()", "defined('WP_HOME')"} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("safe WordPress change omitted %q: %s", expected, commands)
		}
	}
	replaceIndex := strings.Index(commands, "search-replace ")
	cacheIndex := strings.Index(commands, "eval "+domainClearSiteCache(site.ID))
	optionIndex := strings.Index(commands, "option update home ")
	if replaceIndex < 0 || cacheIndex <= replaceIndex || optionIndex <= cacheIndex {
		t.Fatal("SQL search-replace must invalidate this site's cached options before option updates")
	}
	dir, err := host.domainJournalDirectory(site.ID, "wp-change")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "state.json"), filepath.Join(dir, "database.sql")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("recovery is not private: %s %v", path, err)
		}
	}
}

func TestDomainChangeRollsBackSQLAndBothVirtualHosts(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.WordPress, true)
	previous, _ := host.inspectDomainVhost(site.ID, false)
	previousTLS, _ := host.inspectDomainVhost(site.ID, true)
	runner.fail = "option update siteurl"
	err := host.ChangeDomain(context.Background(), site, change, "failed-change")
	var recovered *model.DomainChangeError
	if !errors.As(err, &recovered) || !recovered.PreviousRestored || runner.imports != 1 {
		t.Fatalf("domain recovery was not confirmed: %v, imports=%d", err, runner.imports)
	}
	after, _ := host.inspectDomainVhost(site.ID, false)
	afterTLS, _ := host.inspectDomainVhost(site.ID, true)
	if string(after.Content) != string(previous.Content) || string(afterTLS.Content) != string(previousTLS.Content) || !after.Enabled || !afterTLS.Enabled {
		t.Fatal("original domain virtual hosts were not restored")
	}
	count := len(runner.calls)
	err = host.ChangeDomain(context.Background(), site, change, "failed-change")
	if !errors.As(err, &recovered) || len(runner.calls) != count {
		t.Fatal("recovered failed job was reapplied")
	}
}

func TestDomainChangeFailedRecoveryCanResumeSameJournalWithoutReplacingBackup(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.WordPress, true)
	runner.fail, runner.failImport = "option update siteurl", true
	err := host.ChangeDomain(context.Background(), site, change, "resume-change")
	var recovered *model.DomainChangeError
	if err == nil || errors.As(err, &recovered) {
		t.Fatalf("unrecovered failure reported safe: %v", err)
	}
	runner.failImport = false
	if err := host.ChangeDomain(context.Background(), site, change, "resume-change"); !errors.As(err, &recovered) || !recovered.PreviousRestored {
		t.Fatalf("interrupted rollback did not finish recovery: %v", err)
	}
	if runner.exports != 1 {
		t.Fatalf("replay overwrote original database snapshot: %d exports", runner.exports)
	}
	after, _ := host.inspectDomainVhost(site.ID, false)
	if !strings.Contains(string(after.Content), "server_name example.com;") {
		t.Fatal("partial rollback replayed forward onto a potentially damaged database")
	}
}

func TestDomainChangeRejectsUnmanagedPathsAndMismatchedOperation(t *testing.T) {
	for _, scenario := range []string{"http-symlink", "enabled-target", "site-symlink", "journal-symlink", "wrong-domain", "wrong-status", "multisite"} {
		t.Run(scenario, func(t *testing.T) {
			host, runner, site, change := domainFixture(t, model.WordPress, false)
			switch scenario {
			case "http-symlink":
				path := filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "unmanaged"), path); err != nil {
					t.Fatal(err)
				}
			case "enabled-target":
				path := filepath.Join(host.NginxEnabled, "wpx-"+site.ID+".conf")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/nginx/unmanaged.conf", path); err != nil {
					t.Fatal(err)
				}
			case "site-symlink":
				public := filepath.Join(host.SiteRoot, site.ID, "public")
				if err := os.Remove(public); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), public); err != nil {
					t.Fatal(err)
				}
			case "journal-symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(host.DataRoot, "domain-changes")); err != nil {
					t.Fatal(err)
				}
			case "wrong-domain":
				change.PreviousDomain = "wrong.example.com"
			case "wrong-status":
				site.Status = "active"
			case "multisite":
				site.WordPressMultisite = model.MultisiteSubdomains
			}
			if err := host.ChangeDomain(context.Background(), site, change, "unsafe-change"); err == nil {
				t.Fatal("unsafe operation accepted")
			}
			if runner.exports != 0 || runner.imports != 0 {
				t.Fatal("unsafe operation touched WordPress database")
			}
		})
	}
}

func TestDomainChangePreservesStagingGateCacheAndExpertSnippet(t *testing.T) {
	host, _, site, change := domainFixture(t, model.WordPress, false)
	site.Environment, site.ParentSiteID, site.FastCGICacheEnabled = "staging", "parent-site", true
	if err := host.ChangeDomain(context.Background(), site, change, "staging-domain"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"))
	for _, expected := range []string{"auth_basic", "X-Robots-Tag", "fastcgi_cache WPX", "include " + host.NginxSnippetRoot} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("site feature disappeared: %s", expected)
		}
	}
}

func TestDomainActivationReplayNeverReimportsOrRewritesDatabase(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.WordPress, true)
	reloads := 0
	runner.observe = func(command string) {
		if strings.Contains(command, "reload nginx.service") {
			reloads++
			if reloads == 2 {
				runner.fail = "reload nginx.service"
			}
		}
	}
	err := host.ChangeDomain(context.Background(), site, change, "activation-retry")
	var recovered *model.DomainChangeError
	if err == nil || errors.As(err, &recovered) || runner.imports != 0 {
		t.Fatalf("ambiguous activation result tried to restore SQL: %v", err)
	}
	dir, _ := host.domainJournalDirectory(site.ID, "activation-retry")
	journal, _, err := readDomainJournal(filepath.Join(dir, "state.json"))
	if err != nil || journal.State != "activating" {
		t.Fatalf("activation boundary missing: %#v %v", journal, err)
	}
	callCount := len(runner.calls)
	runner.observe = nil
	if err := host.ChangeDomain(context.Background(), site, change, "activation-retry"); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.calls[callCount:] {
		if strings.Contains(command, "runuser") {
			t.Fatalf("activation retry touched database accepting new writes: %s", command)
		}
	}
	if runner.exports != 1 || runner.imports != 0 {
		t.Fatal("activation replay crossed its database commit boundary")
	}
}

func TestDomainRollbackTrafficReplayDoesNotRepeatDatabaseImport(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.WordPress, true)
	runner.fail = "option update siteurl"
	if err := host.ChangeDomain(context.Background(), site, change, "restore-traffic"); err == nil {
		t.Fatal("fixture should fail and recover")
	}
	dir, _ := host.domainJournalDirectory(site.ID, "restore-traffic")
	path := filepath.Join(dir, "state.json")
	journal, _, err := readDomainJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	// Model a lost final recovery-marker write after old traffic reopened.
	journal.State = "restoring_traffic"
	if err := writeDomainJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	imports := runner.imports
	var recovered *model.DomainChangeError
	if err := host.ChangeDomain(context.Background(), site, change, "restore-traffic"); !errors.As(err, &recovered) || !recovered.PreviousRestored {
		t.Fatalf("old traffic recovery did not converge: %v", err)
	}
	if runner.imports != imports {
		t.Fatal("SQL snapshot reimported after old domain began accepting writes")
	}
}

func TestDomainJournalRejectsOtherOwnerAndNonPrivateState(t *testing.T) {
	for _, scenario := range []string{"directory-mode", "directory-owner", "state-mode", "state-owner", "config-owner"} {
		t.Run(scenario, func(t *testing.T) {
			if strings.HasSuffix(scenario, "owner") && os.Geteuid() != 0 {
				t.Skip("changing fixture ownership requires root")
			}
			host, _, site, change := domainFixture(t, model.Static, false)
			dir, err := host.domainJournalDirectory(site.ID, "untrusted")
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, "state.json")
			if err := writeDomainJournal(state, domainChangeJournal{Version: 1, SiteID: site.ID, JobKey: "untrusted", Change: change, State: "complete"}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "directory-mode":
				err = os.Chmod(dir, 0755)
			case "directory-owner":
				err = os.Chown(dir, 12345, 12345)
			case "state-mode":
				err = os.Chmod(state, 0644)
			case "state-owner":
				err = os.Chown(state, 12345, 12345)
			case "config-owner":
				err = os.Chown(filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"), 12345, 12345)
				if err == nil {
					err = os.Remove(state) // No completion marker should bypass initial vhost checks.
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := host.ChangeDomain(context.Background(), site, change, "untrusted"); err == nil {
				t.Fatal("untrusted recovery state accepted")
			}
		})
	}
}

func TestDomainJournalPinsDirectoryWhenUnprivilegedParentRenamesIt(t *testing.T) {
	host, runner, site, change := domainFixture(t, model.Static, false)
	directory, err := host.domainJournalDirectory(site.ID, "parent-race")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(host.DataRoot, "domain-changes")
	moved := filepath.Join(host.DataRoot, "moved-recovery")
	outside := t.TempDir()
	changed := false
	runner.observe = func(command string) {
		if changed || !strings.Contains(command, "nginx -t") {
			return
		}
		changed = true
		if err := os.Rename(base, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, base); err != nil {
			t.Fatal(err)
		}
	}
	if err := host.ChangeDomain(context.Background(), site, change, "parent-race"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("root journal write escaped into replaced ancestor: %v", err)
	}
	relative, err := filepath.Rel(base, directory)
	if err != nil {
		t.Fatal(err)
	}
	journal, exists, err := readDomainJournal(filepath.Join(moved, relative, "state.json"))
	if err != nil || !exists || journal.State != "complete" {
		t.Fatalf("pinned original journal did not finish: %#v %v", journal, err)
	}
}

func TestDomainCacheInvalidationHasExactNamespaceAndBoundedCommands(t *testing.T) {
	script := domainClearSiteCache("example-com")
	for _, expected := range []string{
		"WP_REDIS_PREFIX !== 'wpx:example-com:'",
		"redis_instance() instanceof Redis",
		"rawCommand('SCAN', $cursor, 'MATCH', $prefix . '*', 'COUNT', '500')",
		"array_chunk($batch[1], 500)",
		"strpos($key, $prefix) !== 0",
		"rawCommand('UNLINK', ...$keys)",
		"ctype_digit($cursor)",
		"microtime(true) + 60",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("cache invalidation is missing safety boundary %q", expected)
		}
	}
	for _, forbidden := range []string{"wp_cache_flush()", "FLUSHDB", "FLUSHALL", "rawCommand('KEYS'"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("cache invalidation could affect neighboring sites: %q", forbidden)
		}
	}
}
