package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestInspectSecurityLogRejectsFalsePositivesAndInvalidAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	lines := []string{
		`203.0.113.10 - - [08/Sep/2026:10:11:12 +0200] "POST /wp-login.php HTTP/1.1" 200 10 "-" "agent"`,
		`203.0.113.10 - - [08/Sep/2026:10:11:13 +0200] "GET /ordinary?next=/wp-login.php HTTP/1.1" 200 10 "-" "agent"`,
		`203.0.113.10 - - [08/Sep/2026:10:11:14 +0200] "GET /.environment HTTP/1.1" 404 10 "-" "agent"`,
		`2001:db8::7 - - [08/Sep/2026:10:11:15 +0200] "GET /.git/config HTTP/1.1" 404 10 "-" "agent"`,
		`attacker.example - - [08/Sep/2026:10:11:16 +0200] "GET /.env HTTP/1.1" 404 10 "-" "agent"`,
		`203.0.113.11 - - [bad] "GET /xmlrpc.php HTTP/1.1" 200 10 "-" "agent"`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := inspectSecurityLog(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.MalformedLines != 2 {
		t.Fatalf("malformed=%d, want 2", report.MalformedLines)
	}
	if len(report.Sources) != 2 {
		t.Fatalf("sources=%#v", report.Sources)
	}
	var login, scan bool
	for _, source := range report.Sources {
		switch source.IP {
		case "203.0.113.10":
			login = source.LoginAttempts == 1 && source.SensitiveScans == 0 && source.NotFound == 1
		case "2001:db8::7":
			scan = source.SensitiveScans == 1 && source.NotFound == 1 && source.Risk == "medium"
		}
	}
	if !login || !scan {
		t.Fatalf("unexpected classification: %#v", report.Sources)
	}
}

func TestInspectSecurityLogRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside.log")
	if err := os.WriteFile(target, []byte(`203.0.113.8 - - [08/Sep/2026:10:11:12 +0200] "GET /.env HTTP/1.1" 404 0`), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "access.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSecurityLog(context.Background(), link); err == nil {
		t.Fatal("symlinked access log was accepted")
	}
}

func TestApplySecurityWritesManagedConfigurationAndRollsBackValidationFailure(t *testing.T) {
	root := t.TempDir()
	paths := securityPaths{
		nginxGlobal:     filepath.Join(root, "nginx", "conf.d", "wpx-security-zones.conf"),
		nginxSites:      filepath.Join(root, "wpx", "security", "sites"),
		fail2banJails:   filepath.Join(root, "fail2ban", "jail.d"),
		fail2banFilters: filepath.Join(root, "fail2ban", "filter.d"),
	}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	settings := model.DefaultSecuritySettings()
	settings.Enabled = true
	runner := &recordRunner{}
	if err := applySecurity(context.Background(), runner, paths, site, settings); err != nil {
		t.Fatal(err)
	}
	jail := filepath.Join(paths.fail2banJails, "wpx-wp-site.local")
	content, err := os.ReadFile(jail)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"banaction = nftables[type=multiport]", "bantime.increment = true", "[wpx-wp-site-login]", "enabled = true"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("jail missing %q: %s", expected, content)
		}
	}
	previous := append([]byte(nil), content...)
	runner.fail = "/usr/sbin/nginx"
	settings.Burst404Protection = false
	if err := applySecurity(context.Background(), runner, paths, site, settings); err == nil {
		t.Fatal("validation failure was ignored")
	}
	after, err := os.ReadFile(jail)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(previous) {
		t.Fatal("failed validation did not restore previous jail")
	}
}

func TestApplySecurityRefusesUnmanagedFiles(t *testing.T) {
	root := t.TempDir()
	paths := securityPaths{
		nginxGlobal:     filepath.Join(root, "nginx", "conf.d", "wpx-security-zones.conf"),
		nginxSites:      filepath.Join(root, "wpx", "security", "sites"),
		fail2banJails:   filepath.Join(root, "fail2ban", "jail.d"),
		fail2banFilters: filepath.Join(root, "fail2ban", "filter.d"),
	}
	if err := os.MkdirAll(paths.fail2banJails, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.fail2banJails, "wpx-wp-site.local"), []byte("operator config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	settings := model.DefaultSecuritySettings()
	settings.Enabled = true
	if err := applySecurity(context.Background(), &recordRunner{}, paths, site, settings); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("unmanaged jail error=%v", err)
	}
}

func TestSecurityDefaultsPreserveEnabledIncludeAndInjectOnce(t *testing.T) {
	root := t.TempDir()
	host := &Host{
		SecurityNginxRoot:       filepath.Join(root, "security", "sites"),
		SecurityNginxGlobal:     filepath.Join(root, "nginx", "conf.d", "zones.conf"),
		SecurityFail2banJails:   filepath.Join(root, "fail2ban", "jail.d"),
		SecurityFail2banFilters: filepath.Join(root, "fail2ban", "filter.d"),
	}
	site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	if err := host.EnsureSecurityDefaults(site); err != nil {
		t.Fatal(err)
	}
	includePath := filepath.Join(host.SecurityNginxRoot, site.ID+".conf")
	enabled := securityMarker + "limit_req zone=wpx_login burst=5 nodelay;\n"
	if err := os.WriteFile(includePath, []byte(enabled), 0644); err != nil {
		t.Fatal(err)
	}
	if err := host.EnsureSecurityDefaults(site); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(includePath)
	if err != nil || string(content) != enabled {
		t.Fatalf("defaults replaced active settings: content=%q error=%v", content, err)
	}
	configuration := ownershipMarker + "server {\n    server_name wp.example.com;\n}\n"
	decorated, err := host.InjectSiteSecurityNginx(site, configuration)
	if err != nil {
		t.Fatal(err)
	}
	decoratedAgain, err := host.InjectSiteSecurityNginx(site, decorated)
	if err != nil {
		t.Fatal(err)
	}
	include := "include " + includePath + ";"
	if strings.Count(decoratedAgain, include) != 1 {
		t.Fatalf("security include count is not one: %s", decoratedAgain)
	}
}

func TestInstallSecurityUsesOnlyFixedPackagesAndStartsService(t *testing.T) {
	runner := &recordRunner{}
	if err := installSecurity(context.Background(), runner, false); err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		joined = append(joined, strings.Join(call, " "))
	}
	want := []string{
		"/usr/bin/apt-get update",
		"/usr/bin/apt-get install -y --no-install-recommends fail2ban nftables",
		"/usr/bin/systemctl enable --now fail2ban.service",
	}
	if strings.Join(joined, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected installation commands: %v", joined)
	}
}

func TestSecurityDefaultsRejectAncestorAndTargetSymlinks(t *testing.T) {
	t.Run("ancestor", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "redirect")); err != nil {
			t.Fatal(err)
		}
		paths := securityPaths{
			nginxGlobal:     filepath.Join(root, "nginx", "zones.conf"),
			nginxSites:      filepath.Join(root, "redirect", "sites"),
			fail2banJails:   filepath.Join(root, "fail2ban", "jails"),
			fail2banFilters: filepath.Join(root, "fail2ban", "filters"),
		}
		site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
		if err := ensureSecurityDefaults(paths, site); err == nil {
			t.Fatal("ancestor symlink was followed")
		}
		if _, err := os.Stat(filepath.Join(outside, "sites")); !os.IsNotExist(err) {
			t.Fatalf("outside path was created: %v", err)
		}
	})
	t.Run("target", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		paths := securityPaths{
			nginxGlobal:     filepath.Join(root, "nginx", "zones.conf"),
			nginxSites:      filepath.Join(root, "security", "sites"),
			fail2banJails:   filepath.Join(root, "fail2ban", "jails"),
			fail2banFilters: filepath.Join(root, "fail2ban", "filters"),
		}
		for _, dir := range []string{paths.nginxSites, paths.fail2banJails, paths.fail2banFilters, filepath.Dir(paths.nginxGlobal)} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}
		outsideFile := filepath.Join(outside, "operator.local")
		original := securityMarker + "operator content\n"
		if err := os.WriteFile(outsideFile, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outsideFile, filepath.Join(paths.fail2banJails, "wpx-wp-site.local")); err != nil {
			t.Fatal(err)
		}
		site := model.Site{ID: "wp-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
		settings := model.DefaultSecuritySettings()
		settings.Enabled = true
		runner := &recordRunner{}
		if err := applySecurity(context.Background(), runner, paths, site, settings); err == nil {
			t.Fatal("target symlink was followed")
		}
		if len(runner.calls) != 0 {
			t.Fatalf("runner called after symlink rejection: %v", runner.calls)
		}
		content, err := os.ReadFile(outsideFile)
		if err != nil || string(content) != original {
			t.Fatalf("outside file changed: %q, %v", content, err)
		}
	})
}
