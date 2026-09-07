package provision

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/crypto/bcrypt"
)

func accessTestHost(t *testing.T, runner Runner) *Host {
	host := testHost(t, runner)
	host.AccessRoot = filepath.Join(t.TempDir(), "access", "sites")
	host.CloudflareRangesTTL = time.Hour
	return host
}

func TestInjectSiteAccessNginxAddsIncludeAndPublicChallenge(t *testing.T) {
	host := accessTestHost(t, &recordRunner{})
	site := model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}
	configuration, err := host.InjectSiteAccessNginx(site, renderStatic(site, filepath.Join(host.SiteRoot, site.ID, "public")))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"include " + filepath.Join(host.AccessRoot, site.ID+".conf") + ";",
		"location ^~ /.well-known/acme-challenge/ { auth_basic off; allow all;",
	} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("configuration missing %q: %s", expected, configuration)
		}
	}
}

func TestApplySiteAccessRollsBackFilesWhenNginxRejectsChange(t *testing.T) {
	runner := &recordRunner{fail: "/usr/sbin/nginx"}
	host := accessTestHost(t, runner)
	if err := os.MkdirAll(host.NginxAvailable, 0755); err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static, Status: "active"}
	old := renderStatic(site, filepath.Join(host.SiteRoot, site.ID, "public"))
	vhost := filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf")
	if err := atomicWrite(vhost, []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("a-long-test-password"), 12)
	settings := model.SiteAccessSettings{SiteID: site.ID, BasicAuthEnabled: true, Username: "visitor", PasswordHash: string(hash), Status: "pending"}
	if err := host.ApplySiteAccess(context.Background(), site, settings); err == nil {
		t.Fatal("injected Nginx validation failure was ignored")
	}
	content, err := os.ReadFile(vhost)
	if err != nil || string(content) != old {
		t.Fatalf("vhost was not restored: err=%v content=%q", err, content)
	}
	for _, path := range []string{filepath.Join(host.AccessRoot, site.ID+".conf"), filepath.Join(host.AccessRoot, site.ID+".htpasswd")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new access file survived rollback: %s err=%v", path, err)
		}
	}
}

func TestCloudflareRangesNeverFailOpen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"ipv4_cidrs":[],"ipv6_cidrs":[]}}`))
	}))
	defer server.Close()
	host := accessTestHost(t, &recordRunner{})
	host.CloudflareRangesURL = server.URL
	host.CloudflareHTTPClient = server.Client()
	ranges := host.cloudflareRanges(context.Background())
	if len(ranges.IPv4) == 0 || len(ranges.IPv6) == 0 {
		t.Fatalf("invalid refresh produced empty allowlist: %#v", ranges)
	}
}

func TestEnsureAccessDefaultsRefusesUnmanagedFile(t *testing.T) {
	host := accessTestHost(t, &recordRunner{})
	if err := os.MkdirAll(host.AccessRoot, 0755); err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}
	path := filepath.Join(host.AccessRoot, site.ID+".conf")
	if err := os.WriteFile(path, []byte("deny all;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := host.EnsureAccessDefaults(context.Background(), site); err == nil {
		t.Fatal("unmanaged include was overwritten")
	}
}

func TestEnsureAccessDefaultsRejectsSymlinkedRootAndTarget(t *testing.T) {
	for _, scenario := range []string{"root", "target"} {
		t.Run(scenario, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0755); err != nil {
				t.Fatal(err)
			}
			host := accessTestHost(t, &recordRunner{})
			if scenario == "root" {
				link := filepath.Join(base, "linked-access")
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
				host.AccessRoot = filepath.Join(link, "sites")
			} else {
				if err := os.MkdirAll(host.AccessRoot, 0755); err != nil {
					t.Fatal(err)
				}
				outsideFile := filepath.Join(outside, "unchanged")
				if err := os.WriteFile(outsideFile, []byte(accessOwnershipMarker+"deny all;\n"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideFile, filepath.Join(host.AccessRoot, "access-site.conf")); err != nil {
					t.Fatal(err)
				}
			}
			site := model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static}
			if err := host.EnsureAccessDefaults(context.Background(), site); err == nil {
				t.Fatal("symlinked access path was accepted")
			}
			if scenario == "target" {
				content, err := os.ReadFile(filepath.Join(outside, "unchanged"))
				if err != nil || string(content) != accessOwnershipMarker+"deny all;\n" {
					t.Fatalf("symlink target changed: content=%q err=%v", content, err)
				}
			}
		})
	}
}

func TestApplySiteAccessRejectsSymlinkedVhost(t *testing.T) {
	host := accessTestHost(t, &recordRunner{})
	if err := os.MkdirAll(host.NginxAvailable, 0755); err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "access-site", Domain: "access.example.com", Kind: model.Static, Status: "active"}
	outside := filepath.Join(t.TempDir(), "outside.conf")
	original := renderStatic(site, filepath.Join(host.SiteRoot, site.ID, "public"))
	if err := os.WriteFile(outside, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(host.NginxAvailable, "wpx-"+site.ID+".conf")); err != nil {
		t.Fatal(err)
	}
	if err := host.ApplySiteAccess(context.Background(), site, model.SiteAccessSettings{SiteID: site.ID, CloudflareOnly: true, Status: "pending"}); err == nil {
		t.Fatal("symlinked vhost crossed the root boundary")
	}
	content, err := os.ReadFile(outside)
	if err != nil || string(content) != original {
		t.Fatalf("symlinked vhost target changed: content=%q err=%v", content, err)
	}
}
