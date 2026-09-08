//go:build linux

package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

func TestMagicLoginUsesStableHandlerAndPrivateCapabilities(t *testing.T) {
	for _, scenario := range []struct {
		name, tlsStatus, scheme string
	}{
		{name: "without TLS", scheme: "http"},
		{name: "active TLS", tlsStatus: "active", scheme: "https"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			host := testHost(t, &recordRunner{})
			site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", TLSStatus: scenario.tlsStatus, Status: "active"}
			publicDir := filepath.Join(host.SiteRoot, site.ID, "public")
			if err := os.MkdirAll(publicDir, 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(publicDir, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
				t.Fatal(err)
			}
			url, expires, err := host.MagicLogin(context.Background(), site)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := urlpkg.Parse(url)
			if err != nil || parsed.Scheme != scenario.scheme || parsed.Host != "example.com" || parsed.Path != "/"+magicLoginHandlerName || parsed.RawQuery != "" || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(parsed.Fragment) {
				t.Fatalf("unexpected login URL %q", url)
			}
			if time.Until(expires) < 80*time.Second || time.Until(expires) > 95*time.Second {
				t.Fatalf("unexpected expiry %v", expires)
			}
			content, err := os.ReadFile(filepath.Join(publicDir, magicLoginHandlerName))
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{"history.replaceState", "form.method = 'post'", "Referrer-Policy: no-referrer", "Cache-Control: no-store", "<noscript>", "rename($pending, $claimed)", "wp_set_auth_cookie"} {
				if !strings.Contains(string(content), expected) {
					t.Fatalf("handler missing %q: %s", expected, content)
				}
			}
			if strings.Contains(string(content), parsed.Fragment) || strings.Contains(parsed.Path, parsed.Fragment) {
				t.Fatal("capability leaked outside the URL fragment")
			}
			info, err := os.Stat(filepath.Join(publicDir, magicLoginHandlerName))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("handler mode is %o", info.Mode().Perm())
			}
			digest := sha256.Sum256([]byte(parsed.Fragment))
			statePath := filepath.Join(host.SiteRoot, site.ID, "tmp", magicLoginTokenDir, hex.EncodeToString(digest[:])+".token")
			state, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if expiry, valid := parseMagicLoginState(state); !valid || expiry.Unix() != expires.Unix() {
				t.Fatalf("invalid private state %q", state)
			}
			if stateInfo, _ := os.Stat(statePath); stateInfo.Mode().Perm() != 0600 {
				t.Fatalf("capability mode is %o", stateInfo.Mode().Perm())
			}
			if dirInfo, _ := os.Stat(filepath.Dir(statePath)); dirInfo.Mode().Perm() != 0700 {
				t.Fatalf("capability directory mode is %o", dirInfo.Mode().Perm())
			}
			if _, _, err := host.MagicLogin(context.Background(), site); err != nil {
				t.Fatal(err)
			}
			publicEntries, _ := os.ReadDir(publicDir)
			if len(publicEntries) != 2 {
				t.Fatalf("repeated access accumulated public files: %#v", publicEntries)
			}
			privateEntries, _ := os.ReadDir(filepath.Dir(statePath))
			if len(privateEntries) != 2 {
				t.Fatalf("expected two bounded private capabilities, got %#v", privateEntries)
			}
		})
	}
}

func TestMagicLoginRefusesSymlinkedPublicDirectory(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	siteDir := filepath.Join(host.SiteRoot, site.ID)
	outside := t.TempDir()
	if err := os.MkdirAll(siteDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(siteDir, "public")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.MagicLogin(context.Background(), site); err == nil {
		t.Fatal("symlinked public directory was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("broker wrote outside site root: %#v", entries)
	}
}

func TestMagicLoginCleansOnlyExpiredExactLegacyScripts(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "legacy-site", Domain: "legacy.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(host.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expiredName := "wpx-login-" + strings.Repeat("a", 64) + ".php"
	activeName := "wpx-login-" + strings.Repeat("b", 64) + ".php"
	unmanagedName := "wpx-login-" + strings.Repeat("c", 64) + ".php"
	symlinkName := "wpx-login-" + strings.Repeat("d", 64) + ".php"
	if err := os.WriteFile(filepath.Join(public, expiredName), []byte(magicLoginScript(now.Add(-time.Minute))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, activeName), []byte(magicLoginScript(now.Add(time.Minute))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, unmanagedName), []byte("<?php // user file"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.php")
	if err := os.WriteFile(outside, []byte(magicLoginScript(now.Add(-time.Minute))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(public, symlinkName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "wp-login.php"), []byte("core"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.MagicLogin(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(public, expiredName)); !os.IsNotExist(err) {
		t.Fatalf("expired managed legacy file remains: %v", err)
	}
	for _, name := range []string{activeName, unmanagedName, symlinkName, "wp-login.php", magicLoginHandlerName} {
		if _, err := os.Lstat(filepath.Join(public, name)); err != nil {
			t.Fatalf("safe cleanup removed %s: %v", name, err)
		}
	}
}

func TestMagicLoginRefusesFIFOAndHardlinkedStableHandler(t *testing.T) {
	for _, scenario := range []string{"fifo", "hardlink"} {
		t.Run(scenario, func(t *testing.T) {
			host := testHost(t, &recordRunner{})
			site := model.Site{ID: "collision-site", Domain: "collision.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
			public := filepath.Join(host.SiteRoot, site.ID, "public")
			if err := os.MkdirAll(public, 0750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(public, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
				t.Fatal(err)
			}
			handler := filepath.Join(public, magicLoginHandlerName)
			if scenario == "fifo" {
				if err := unix.Mkfifo(handler, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(handler, []byte(magicLoginHandlerScript()), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(handler, filepath.Join(public, "user-copy.php")); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := host.MagicLogin(context.Background(), site); err == nil {
				t.Fatal("unsafe stable handler was accepted")
			}
		})
	}
}

func TestMagicLoginRefusesSymlinkedPrivateCapabilityDirectory(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "private-link", Domain: "private.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(host.SiteRoot, site.ID, "public")
	tmp := filepath.Join(host.SiteRoot, site.ID, "tmp")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "wp-load.php"), []byte("<?php\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(tmp, magicLoginTokenDir)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := host.MagicLogin(context.Background(), site); err == nil {
		t.Fatal("symlinked capability directory was accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("capability escaped the site directory")
	}
}

func TestMagicLoginCleanupPreservesLiveClaimAndUnsafeEntries(t *testing.T) {
	directory := t.TempDir()
	dirFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirFD)
	now := time.Now().UTC().Truncate(time.Second)
	live := strings.Repeat("a", 64) + ".used"
	expired := strings.Repeat("b", 64) + ".used"
	symlink := strings.Repeat("c", 64) + ".used"
	hardlink := strings.Repeat("d", 64) + ".token"
	if err := os.WriteFile(filepath.Join(directory, live), []byte("v1\n"+strconv.FormatInt(now.Add(time.Minute).Unix(), 10)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, expired), []byte("v1\n"+strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("v1\n"+strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, symlink)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(directory, hardlink)); err != nil {
		t.Fatal(err)
	}
	if err := cleanupMagicLoginState(dirFD, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, expired)); !os.IsNotExist(err) {
		t.Fatalf("expired state remains: %v", err)
	}
	for _, name := range []string{live, symlink, hardlink} {
		if _, err := os.Lstat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("cleanup removed %s: %v", name, err)
		}
	}
}
