package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestDisableLastPHPSiteRemovesTrafficAndStopsVersion(t *testing.T) {
	runner := &recordRunner{}
	host := testHost(t, runner)
	phpRoot := filepath.Join(t.TempDir(), "php")
	host.PHP = &AptPHPRuntime{Runner: runner, ConfigRoot: phpRoot, RunRoot: filepath.Join(t.TempDir(), "run"), SnippetRoot: host.PHPSnippetRoot}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "disabling"}
	for _, name := range []string{"wpx-example-com.conf", "wpx-example-com-tls.conf"} {
		available := filepath.Join(host.NginxAvailable, name)
		if err := os.MkdirAll(filepath.Dir(available), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(available, []byte(ownershipMarker+"server {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(host.NginxEnabled, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(available, filepath.Join(host.NginxEnabled, name)); err != nil {
			t.Fatal(err)
		}
	}
	poolPath := filepath.Join(phpRoot, "8.4", "fpm", "pool.d", "wpx-example-com.conf")
	if err := os.MkdirAll(filepath.Dir(poolPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolPath, []byte(phpOwnershipMarker+"[wpx-example-com]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := host.Disable(context.Background(), site, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wpx-example-com.conf", "wpx-example-com-tls.conf"} {
		if _, err := os.Lstat(filepath.Join(host.NginxEnabled, name)); !os.IsNotExist(err) {
			t.Fatalf("enabled Nginx link remains: %s", name)
		}
	}
	if _, err := os.Stat(poolPath); !os.IsNotExist(err) {
		t.Fatalf("PHP pool remains: %v", err)
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, strings.Join(call, " "))
	}
	if !strings.Contains(strings.Join(commands, "\n"), "/usr/bin/systemctl stop php8.4-fpm.service") {
		t.Fatalf("unused PHP service was not stopped: %#v", runner.calls)
	}
}
