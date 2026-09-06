package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type deletionIdentities struct {
	identity Identity
	exists   bool
	removals int
	fail     bool
}

func (m *deletionIdentities) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{}, errors.New("deletion must not recreate an identity")
}

func (m *deletionIdentities) InspectSite(context.Context, model.Site, string) (Identity, bool, error) {
	return m.identity, m.exists, nil
}

func (m *deletionIdentities) StopSite(_ context.Context, _ model.Site, _ string, identity Identity) error {
	if m.exists && identity != m.identity {
		return errors.New("identity changed")
	}
	return nil
}

func (m *deletionIdentities) RemoveSite(_ context.Context, _ model.Site, _ string, identity Identity) error {
	if m.fail {
		return errors.New("injected identity removal failure")
	}
	if m.exists && identity != m.identity {
		return errors.New("identity changed")
	}
	if m.exists {
		m.removals++
		m.exists = false
	}
	return nil
}

type deletionSQL struct {
	statements []string
	fail       bool
}

type deletionFailRunner struct {
	recordRunner
	command string
	failed  bool
}

func (r *deletionFailRunner) Run(ctx context.Context, executable string, args ...string) error {
	if err := r.recordRunner.Run(ctx, executable, args...); err != nil {
		return err
	}
	if !r.failed && strings.Join(append([]string{executable}, args...), " ") == r.command {
		r.failed = true
		return errors.New("injected interruption before runtime reload")
	}
	return nil
}

func (s *deletionSQL) Execute(_ context.Context, statement string) error {
	s.statements = append(s.statements, statement)
	if s.fail {
		return errors.New("injected database failure")
	}
	return nil
}

func deletionHost(t *testing.T, site model.Site) (*Host, *recordRunner, *deletionIdentities) {
	t.Helper()
	runner := &recordRunner{}
	host := testHost(t, runner)
	identities := &deletionIdentities{identity: Identity{Name: accountName(site.ID), UID: os.Getuid(), GID: os.Getgid()}, exists: true}
	host.Identities = identities
	writeDeletionFixture(t, filepath.Join(host.SiteRoot, site.ID, "public", "index.html"), "application", 0640)
	writeDeletionFixture(t, filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"), ownershipMarker+"server {}\n", 0644)
	if err := os.MkdirAll(host.NginxEnabled, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"), filepath.Join(host.NginxEnabled, "wpx-"+site.ID+".conf")); err != nil {
		t.Fatal(err)
	}
	return host, runner, identities
}

func writeDeletionFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSiteIsConfinedAndCompletionReplayDoesNothing(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static, Status: "deleting"}
	host, runner, identities := deletionHost(t, site)
	sibling := filepath.Join(host.SiteRoot, "another-site", "public", "index.html")
	writeDeletionFixture(t, sibling, "other site", 0640)
	certificate := filepath.Join(host.CertificateRoot, site.Domain, "fullchain.pem")
	writeDeletionFixture(t, certificate, "retained certificate", 0644)
	backup := filepath.Join(host.DataRoot, "retained-backup", "snapshot")
	writeDeletionFixture(t, backup, "retained backup", 0600)
	// Nested application symlinks are unlinked, never traversed.
	if err := os.Symlink(filepath.Dir(sibling), filepath.Join(host.SiteRoot, site.ID, "public", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := host.DeleteSite(context.Background(), site, true, "delete-job"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(host.SiteRoot, site.ID), filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf"), filepath.Join(host.NginxEnabled, "wpx-"+site.ID+".conf")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted resource remains %s: %v", path, err)
		}
	}
	for _, path := range []string{sibling, certificate, backup} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unrelated retained resource changed %s: %v", path, err)
		}
	}
	if identities.removals != 1 {
		t.Fatalf("unexpected account removals: %d", identities.removals)
	}
	calls := len(runner.calls)
	if err := host.DeleteSite(context.Background(), site, true, "delete-job"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != calls || identities.removals != 1 {
		t.Fatal("completed deletion replay repeated host changes")
	}
}

func TestDeleteSiteRejectsUnmanagedAndRedirectedPathsBeforeStoppingTraffic(t *testing.T) {
	for _, hostile := range []string{"site-symlink", "nginx-symlink", "nginx-marker", "enabled-link", "root-symlink", "missing-identity", "site-id", "journal-symlink"} {
		t.Run(hostile, func(t *testing.T) {
			site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static, Status: "deleting"}
			host, runner, identities := deletionHost(t, site)
			outside := filepath.Join(t.TempDir(), "keep")
			writeDeletionFixture(t, outside, "do not remove", 0600)
			path := filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf")
			switch hostile {
			case "site-symlink":
				siteDir := filepath.Join(host.SiteRoot, site.ID)
				if err := os.Rename(siteDir, siteDir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), siteDir); err != nil {
					t.Fatal(err)
				}
			case "nginx-symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "nginx-marker":
				writeDeletionFixture(t, path, "unmanaged config", 0644)
			case "enabled-link":
				link := filepath.Join(host.NginxEnabled, filepath.Base(path))
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
			case "root-symlink":
				if err := os.Rename(host.SiteRoot, host.SiteRoot+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(host.SiteRoot+"-saved", host.SiteRoot); err != nil {
					t.Fatal(err)
				}
			case "missing-identity":
				identities.exists = false
			case "site-id":
				site.ID = "../another-site"
			case "journal-symlink":
				if err := os.MkdirAll(host.DataRoot, 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Join(host.DataRoot, "deletions")); err != nil {
					t.Fatal(err)
				}
			}
			if err := host.DeleteSite(context.Background(), site, true, "hostile-job"); err == nil {
				t.Fatal("hostile resource accepted")
			}
			if len(runner.calls) != 0 || identities.removals != 0 {
				t.Fatal("destructive work started before all ownership checks")
			}
			if content, err := os.ReadFile(outside); err != nil || string(content) != "do not remove" {
				t.Fatal("outside resource changed")
			}
		})
	}
}

func TestDeleteWordPressResumesAfterDatabaseFailureWithoutRecreatingAccount(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "deleting"}
	host, runner, identities := deletionHost(t, site)
	phpRoot := filepath.Join(t.TempDir(), "php")
	host.PHP = &AptPHPRuntime{Runner: runner, ConfigRoot: phpRoot, RunRoot: filepath.Join(t.TempDir(), "run"), SnippetRoot: host.PHPSnippetRoot}
	writeDeletionFixture(t, filepath.Join(phpRoot, "8.4", "fpm", "pool.d", "wpx-"+site.ID+".conf"), phpOwnershipMarker+"[site]\n", 0644)
	sql := &deletionSQL{fail: true}
	database := &MariaDB{SQL: sql, SecretsRoot: filepath.Join(host.DataRoot, "secrets", "sites")}
	host.Database = database
	name := deletionDatabaseName(site.ID)
	credentials, err := json.Marshal(DatabaseCredentials{Name: name, User: name, Password: "secret", Host: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(database.SecretsRoot, site.ID+".json")
	writeDeletionFixture(t, secretPath, string(credentials), 0600)
	if err := host.DeleteSite(context.Background(), site, true, "wp-delete-job"); err == nil {
		t.Fatal("database failure was hidden")
	}
	if !identities.exists || identities.removals != 0 {
		t.Fatal("account must reserve its UID while a failed deletion leaves private files")
	}
	if _, err := os.Stat(filepath.Join(host.SiteRoot, site.ID, "public", "index.html")); err != nil {
		t.Fatal("files removed before database deletion succeeded")
	}
	sql.fail = false
	if err := host.DeleteSite(context.Background(), site, false, "wp-delete-job"); err != nil {
		t.Fatal(err)
	}
	if identities.removals != 1 || len(sql.statements) != 2 || sql.statements[0] != sql.statements[1] {
		t.Fatal("partial replay did not preserve exact account/database targets")
	}
	if _, err := os.Stat(secretPath); !os.IsNotExist(err) {
		t.Fatal("site database secret remains")
	}
}

func TestDeleteSiteKeepsSharedPHPBranchAndStopsUnusedBranch(t *testing.T) {
	for _, shared := range []bool{true, false} {
		t.Run(map[bool]string{true: "shared", false: "last-pool"}[shared], func(t *testing.T) {
			site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "deleting"}
			host, runner, _ := deletionHost(t, site)
			phpRoot := filepath.Join(t.TempDir(), "php")
			host.PHP = &AptPHPRuntime{Runner: runner, ConfigRoot: phpRoot, RunRoot: filepath.Join(t.TempDir(), "run"), SnippetRoot: host.PHPSnippetRoot}
			poolDir := filepath.Join(phpRoot, "8.4", "fpm", "pool.d")
			writeDeletionFixture(t, filepath.Join(poolDir, "wpx-"+site.ID+".conf"), phpOwnershipMarker+"[site]\n", 0644)
			if shared {
				writeDeletionFixture(t, filepath.Join(poolDir, "expert-pool.conf"), "unrelated pool", 0644)
			}
			if err := host.DeleteSite(context.Background(), site, true, "php-job"); err != nil {
				t.Fatal(err)
			}
			var commands []string
			for _, call := range runner.calls {
				commands = append(commands, strings.Join(call, " "))
			}
			stopped := strings.Contains(strings.Join(commands, "\n"), "disable --now php8.4-fpm.service")
			if stopped == shared {
				t.Fatalf("shared=%v commands=%v", shared, commands)
			}
			if shared {
				if _, err := os.Stat(filepath.Join(poolDir, "expert-pool.conf")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDeleteWordPressRejectsUnrelatedDatabase(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "deleting"}
	host, runner, identities := deletionHost(t, site)
	host.PHP = &AptPHPRuntime{Runner: runner, ConfigRoot: filepath.Join(t.TempDir(), "php"), RunRoot: filepath.Join(t.TempDir(), "run")}
	sql := &deletionSQL{}
	host.Database = &MariaDB{SQL: sql, SecretsRoot: filepath.Join(host.DataRoot, "secrets", "sites")}
	writeDeletionFixture(t, filepath.Join(host.DataRoot, "secrets", "sites", site.ID+".json"), `{"name":"unrelated","user":"unrelated","password":"secret","host":"localhost"}`, 0600)
	if err := host.DeleteSite(context.Background(), site, true, "db-hostile"); err == nil {
		t.Fatal("unrelated database accepted")
	}
	if len(sql.statements) != 0 || len(runner.calls) != 0 || identities.removals != 0 {
		t.Fatal("ownership refusal changed host")
	}
}

func TestDeleteSiteRetryRejectsChangedRootsAndIdentity(t *testing.T) {
	for _, change := range []string{"root", "identity"} {
		t.Run(change, func(t *testing.T) {
			site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static, Status: "deleting"}
			host, runner, identities := deletionHost(t, site)
			identities.fail = true
			if err := host.DeleteSite(context.Background(), site, false, "bound-operation"); err == nil {
				t.Fatal("fixture did not interrupt")
			}
			identities.fail = false
			calls := len(runner.calls)
			if change == "root" {
				host.NginxAvailable = filepath.Join(t.TempDir(), "different-configs")
			} else {
				identities.identity.UID++
			}
			if err := host.DeleteSite(context.Background(), site, false, "bound-operation"); err == nil {
				t.Fatal("changed deletion ownership accepted")
			}
			if len(runner.calls) != calls || identities.removals != 0 {
				t.Fatal("retry changed resources before checking saved operation")
			}
		})
	}
}

func TestDeleteSiteRejectsJournalInsideSiteTree(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static, Status: "deleting"}
	host, runner, _ := deletionHost(t, site)
	host.DataRoot = filepath.Join(host.SiteRoot, site.ID)
	if err := host.DeleteSite(context.Background(), site, false, "overlapping-roots"); err == nil {
		t.Fatal("journal inside deleted tree accepted")
	}
	if len(runner.calls) != 0 {
		t.Fatal("overlapping configuration modified host")
	}
}

func TestDeleteSiteReplayRemembersRemovedPoolOnNonselectedBranch(t *testing.T) {
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "deleting"}
	host, _, _ := deletionHost(t, site)
	runner := &deletionFailRunner{command: "/usr/bin/systemctl disable --now php8.5-fpm.service"}
	host.Runner = runner
	phpRoot := filepath.Join(t.TempDir(), "php")
	host.PHP = &AptPHPRuntime{Runner: runner, ConfigRoot: phpRoot, RunRoot: filepath.Join(t.TempDir(), "run"), SnippetRoot: host.PHPSnippetRoot}
	// A failed version change can leave a pool on the target branch while the
	// persisted site's selected version is still 8.4.
	pool := filepath.Join(phpRoot, "8.5", "fpm", "pool.d", "wpx-"+site.ID+".conf")
	writeDeletionFixture(t, pool, phpOwnershipMarker+"[site]\n", 0644)
	if err := host.DeleteSite(context.Background(), site, false, "interrupted-pool-delete"); err == nil {
		t.Fatal("runtime interruption was hidden")
	}
	if _, err := os.Stat(pool); !os.IsNotExist(err) {
		t.Fatal("fixture did not reach removed-pool interruption boundary")
	}
	if err := host.DeleteSite(context.Background(), site, false, "interrupted-pool-delete"); err != nil {
		t.Fatal(err)
	}
	stops := 0
	for _, call := range runner.calls {
		if strings.Join(call, " ") == runner.command {
			stops++
		}
	}
	if stops != 2 {
		t.Fatalf("nonselected branch not reconciled again after interruption: %v", runner.calls)
	}
}

func TestDeleteSiteRejectsMountsIncludingSameDeviceBindMount(t *testing.T) {
	for _, mounted := range []string{"/srv/wpx/example-com", "/srv/wpx/example-com/public/uploads", `/srv/wpx/example-com/public/my\040files`} {
		line := "1 0 8:1 / " + mounted + " rw - ext4 /dev/vda1 rw\n"
		if err := validateDeletionMounts("/srv/wpx/example-com", line); err == nil {
			t.Fatalf("mount accepted: %s", mounted)
		}
	}
	if err := validateDeletionMounts("/srv/wpx/example-com", "1 0 8:1 / /srv/wpx/other rw - ext4 /dev/vda1 rw\n"); err != nil {
		t.Fatal(err)
	}
}

func TestDeletionWaitsForPHPListenerToStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitDeletionSocketStopped(ctx, path); err == nil {
		t.Fatal("still-active runtime listener was accepted as stopped")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitDeletionSocketStopped(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}
