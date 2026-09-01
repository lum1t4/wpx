//go:build linux

package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestMagicLoginWritesShortLivedSiteOwnedBootstrap(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
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
	if !strings.HasPrefix(url, "http://example.com/wpx-login-") || !strings.HasSuffix(url, ".php") {
		t.Fatalf("unexpected login URL %q", url)
	}
	if time.Until(expires) < 80*time.Second || time.Until(expires) > 95*time.Second {
		t.Fatalf("unexpected expiry %v", expires)
	}
	name := strings.TrimPrefix(url, "http://example.com/")
	content, err := os.ReadFile(filepath.Join(publicDir, name))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"unlink(__FILE__)", "role' => 'administrator'", "wp_set_auth_cookie", "wp_safe_redirect(admin_url())"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("bootstrap missing %q: %s", expected, content)
		}
	}
	info, err := os.Stat(filepath.Join(publicDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("bootstrap mode is %o", info.Mode().Perm())
	}
}

func TestMagicLoginRefusesSymlinkedPublicDirectory(t *testing.T) {
	host := testHost(t, &recordRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
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
