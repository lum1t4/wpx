package provision

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPHPMyAdminExtractionRejectsEscapesAndOmitsSetup(t *testing.T) {
	makeArchive := func(names []string) []byte {
		var content bytes.Buffer
		writer := zip.NewWriter(&content)
		for _, name := range names {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte("fixture")); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return content.Bytes()
	}
	prefix := "phpMyAdmin-" + phpMyAdminVersion + "-all-languages/"
	root := filepath.Join(t.TempDir(), "phpmyadmin")
	manager := &PHPMyAdmin{Root: root}
	if err := manager.extractApplication(makeArchive([]string{prefix + "index.php", prefix + "setup/index.php"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "index.php")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "setup", "index.php")); !os.IsNotExist(err) {
		t.Fatalf("setup application was extracted: %v", err)
	}
	unsafe := &PHPMyAdmin{Root: filepath.Join(t.TempDir(), "phpmyadmin")}
	if err := unsafe.extractApplication(makeArchive([]string{prefix + "../escape"})); err == nil {
		t.Fatal("archive traversal path was accepted")
	}
}

func TestDefaultPHPMyAdminStateDoesNotDependOnPrivateWPXParent(t *testing.T) {
	manager := DefaultPHPMyAdmin(ExecRunner{}, "/var/lib/wpx")
	if manager.StateRoot != "/var/lib/wpx-phpmyadmin" || manager.TokenRoot != "/run/wpx-phpmyadmin" {
		t.Fatalf("runtime paths=%q %q", manager.StateRoot, manager.TokenRoot)
	}
}

func TestPHPMyAdminIsLoopbackOnlyAndUsesSingleUseSignon(t *testing.T) {
	manager := DefaultPHPMyAdmin(ExecRunner{}, "/var/lib/wpx")
	nginx := manager.renderNginx()
	if !strings.Contains(nginx, "listen 127.0.0.1:9081") || strings.Contains(nginx, "listen 0.0.0.0") || strings.Contains(nginx, "listen [::]") {
		t.Fatalf("phpMyAdmin listener is not loopback-only:\n%s", nginx)
	}
	config := phpMyAdminConfig(strings.Repeat("a", 43), manager.StateRoot)
	if !strings.Contains(config, "auth_type'] = 'signon'") || !strings.Contains(config, "LogoutURL'] = '/sites'") || !strings.Contains(config, "DefaultLang'] = 'en'") || strings.Contains(config, "auth_type'] = 'cookie'") || strings.Contains(config, "root") {
		t.Fatalf("phpMyAdmin config does not enforce sign-on:\n%s", config)
	}
	pool := manager.renderPool(Identity{Name: "wpx-pma"})
	if !strings.Contains(pool, "php_admin_flag[session.cookie_secure] = on") || !strings.Contains(pool, "php_admin_value[session.cookie_path] = /phpmyadmin/") {
		t.Fatalf("phpMyAdmin session cookies are not HTTPS-scoped:\n%s", pool)
	}
	signon := phpMyAdminSignon(manager.TokenRoot)
	if !strings.Contains(signon, "@unlink($path)") || !strings.Contains(signon, "expires_at") || !strings.Contains(signon, "index.php?lang=en") || strings.Contains(signon, "PMA_single_signon_user'] = 'root'") {
		t.Fatalf("sign-on endpoint is not one-time and scoped:\n%s", signon)
	}
}
