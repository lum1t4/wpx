package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type phpChangeRunner struct {
	calls  []string
	fail   string
	failed bool
}

func (r *phpChangeRunner) Run(_ context.Context, executable string, args ...string) error {
	command := executable + " " + strings.Join(args, " ")
	r.calls = append(r.calls, command)
	if command == r.fail && !r.failed {
		r.failed = true
		return errors.New("injected runtime failure")
	}
	return nil
}

func phpChangeFixture(t *testing.T, runner Runner) (*Host, model.Site, model.PHPVersionChange, string, string) {
	t.Helper()
	host := testHost(t, runner)
	php := &AptPHPRuntime{Runner: runner, ConfigRoot: filepath.Join(t.TempDir(), "php"), RunRoot: filepath.Join(t.TempDir(), "run"), SnippetRoot: host.PHPSnippetRoot, SocketReady: func(context.Context, string) error { return nil }}
	host.PHP = php
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "php_changing"}
	change := model.PHPVersionChange{PreviousVersion: "8.4", Version: "8.5"}
	oldPool := filepath.Join(php.ConfigRoot, "8.4", "fpm", "pool.d", "wpx-example-com.conf")
	newPool := filepath.Join(php.ConfigRoot, "8.5", "fpm", "pool.d", "wpx-example-com.conf")
	if err := os.MkdirAll(filepath.Dir(oldPool), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPool, []byte(phpOwnershipMarker+"[wpx-example-com]\n; existing pool settings\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return host, site, change, oldPool, newPool
}

func TestChangePHPVersionMovesOnlyPoolAndReplaysAfterWorkerInterruption(t *testing.T) {
	runner := &phpChangeRunner{}
	host, site, change, oldPool, newPool := phpChangeFixture(t, runner)
	wordpress := &fakeWordPress{}
	host.WordPress = wordpress
	for range 2 {
		if err := host.ChangePHPVersion(context.Background(), site, change); err != nil {
			t.Fatal(err)
		}
	}
	if wordpress.called {
		t.Fatal("changing PHP must not reinstall or reconfigure WordPress")
	}
	if _, err := os.Stat(oldPool); !os.IsNotExist(err) {
		t.Fatalf("old pool is still active: %v", err)
	}
	if _, err := os.Stat(oldPool + ".wpx-previous"); err != nil {
		t.Fatalf("worker replay lost its recovery pool: %v", err)
	}
	pool, err := os.ReadFile(newPool)
	if err != nil || !strings.Contains(string(pool), "pm = ondemand") || !strings.Contains(string(pool), "wpx-example-com.sock") {
		t.Fatalf("new pool unavailable or did not preserve site socket: %v", err)
	}
	commands := strings.Join(runner.calls, "\n")
	if !strings.Contains(commands, "disable --now php8.4-fpm.service") || !strings.Contains(commands, "enable --now php8.5-fpm.service") {
		t.Fatalf("runtime transition missing: %s", commands)
	}
	for _, command := range runner.calls {
		if strings.HasPrefix(command, "/usr/bin/apt-get") && (strings.Contains(command, "php8.4-") || strings.Contains(command, "php7.")) {
			t.Fatalf("unrequested runtime was installed: %s", command)
		}
	}
}

func TestChangePHPVersionKeepsOtherPoolsRunning(t *testing.T) {
	runner := &phpChangeRunner{}
	host, site, change, oldPool, _ := phpChangeFixture(t, runner)
	neighbor := filepath.Join(filepath.Dir(oldPool), "wpx-another-site.conf")
	if err := os.WriteFile(neighbor, []byte(phpOwnershipMarker+"[another]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := host.ChangePHPVersion(context.Background(), site, change); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(runner.calls, "\n")
	if strings.Contains(commands, "disable --now php8.4-fpm.service") || !strings.Contains(commands, "reload php8.4-fpm.service") {
		t.Fatalf("other site's PHP was stopped: %s", commands)
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Fatal(err)
	}
}

func TestChangePHPVersionRollsBackFPMAndNginxFailures(t *testing.T) {
	for _, failure := range []string{
		"/usr/bin/systemctl disable --now php8.4-fpm.service",
		"/usr/sbin/php-fpm8.5 -t",
		"/usr/bin/systemctl enable --now php8.5-fpm.service",
		"/usr/bin/systemctl reload php8.5-fpm.service",
		"/usr/bin/systemctl is-active --quiet php8.5-fpm.service",
		"/usr/sbin/nginx -t",
		"/usr/bin/systemctl reload nginx.service",
	} {
		t.Run(failure, func(t *testing.T) {
			runner := &phpChangeRunner{fail: failure}
			host, site, change, oldPool, newPool := phpChangeFixture(t, runner)
			before, err := os.ReadFile(oldPool)
			if err != nil {
				t.Fatal(err)
			}
			err = host.ChangePHPVersion(context.Background(), site, change)
			var changeErr *model.PHPVersionChangeError
			if !errors.As(err, &changeErr) || !changeErr.PreviousRestored {
				t.Fatalf("failure must restore the original runtime: %v", err)
			}
			after, err := os.ReadFile(oldPool)
			if err != nil || string(after) != string(before) {
				t.Fatalf("original pool was not restored: %v", err)
			}
			if _, err := os.Stat(newPool); !os.IsNotExist(err) {
				t.Fatalf("failed target pool is still configured: %v", err)
			}
			commands := strings.Join(runner.calls, "\n")
			if !strings.Contains(commands, "disable --now php8.5-fpm.service") || !strings.Contains(commands, "enable --now php8.4-fpm.service") {
				t.Fatalf("runtime rollback missing: %s", commands)
			}
		})
	}
}

func TestChangePHPVersionRejectsUnsafeRequestsBeforeHostChanges(t *testing.T) {
	for _, scenario := range []string{"site traversal", "version traversal", "wrong current version", "EOL unconfirmed", "not changing", "unmanaged pool", "symlinked pool", "unsafe runtime root"} {
		t.Run(scenario, func(t *testing.T) {
			runner := &phpChangeRunner{}
			host, site, change, oldPool, _ := phpChangeFixture(t, runner)
			switch scenario {
			case "site traversal":
				site.ID = "../../outside"
			case "version traversal":
				change.Version = "../../outside"
			case "wrong current version":
				change.PreviousVersion = "8.3"
			case "EOL unconfirmed":
				change.Version = "7.4"
			case "not changing":
				site.Status = "active"
			case "unsafe runtime root":
				host.PHP.(*AptPHPRuntime).ConfigRoot = "/"
			case "unmanaged pool":
				if err := os.WriteFile(oldPool, []byte("[unmanaged]"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlinked pool":
				if err := os.Remove(oldPool); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), oldPool); err != nil {
					t.Fatal(err)
				}
			}
			if err := host.ChangePHPVersion(context.Background(), site, change); err == nil {
				t.Fatal("unsafe change accepted")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("host commands ran for unsafe request: %v", runner.calls)
			}
		})
	}
}

func TestPHPVersionRollbackDoesNotClaimRecoveryWithoutAReadySocket(t *testing.T) {
	runner := &phpChangeRunner{fail: "/usr/sbin/php-fpm8.5 -t"}
	host, site, change, _, _ := phpChangeFixture(t, runner)
	host.PHP.(*AptPHPRuntime).SocketReady = func(context.Context, string) error { return errors.New("socket unavailable") }
	err := host.ChangePHPVersion(context.Background(), site, change)
	var recovery *model.PHPVersionChangeError
	if err == nil || errors.As(err, &recovery) && recovery.PreviousRestored {
		t.Fatalf("broken previous runtime must not unlock site management: %v", err)
	}
}

func TestPHPDownloadFailureKeepsConfirmedCurrentSiteAvailable(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/php-fpm8.5"); err == nil {
		t.Skip("PHP 8.5 is already installed, so no download is needed")
	}
	runner := &phpChangeRunner{fail: "/usr/bin/apt-get install -y --no-install-recommends " + strings.Join(phpPackages("8.5"), " ")}
	host, site, change, oldPool, newPool := phpChangeFixture(t, runner)
	err := host.ChangePHPVersion(context.Background(), site, change)
	var recovery *model.PHPVersionChangeError
	if !errors.As(err, &recovery) || !recovery.PreviousRestored {
		t.Fatalf("healthy old site should stay available after a download failure: %v", err)
	}
	if _, err := os.Stat(oldPool); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(newPool); !os.IsNotExist(err) {
		t.Fatalf("target pool was activated despite failed download: %v", err)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "disable --now php8.4-fpm.service") {
		t.Fatal("old runtime was interrupted before the target was installed")
	}
}
