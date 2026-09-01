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

type wordpressInstallRunner struct{ calls [][]string }

func (r *wordpressInstallRunner) Run(_ context.Context, executable string, args ...string) error {
	r.calls = append(r.calls, append([]string{executable}, args...))
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "config create") {
		for _, argument := range args {
			if strings.HasPrefix(argument, "--path=") {
				publicDir := strings.TrimPrefix(argument, "--path=")
				if err := os.MkdirAll(publicDir, 0750); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(publicDir, "wp-config.php"), []byte("<?php"), 0644)
			}
		}
	}
	if strings.Contains(joined, "core is-installed") {
		return errors.New("not installed")
	}
	return nil
}

func TestWordPressSubdomainMultisiteUsesNetworkInstaller(t *testing.T) {
	runner := &wordpressInstallRunner{}
	root := t.TempDir()
	wpcliPath := filepath.Join(root, "wp-cli.phar")
	if err := os.WriteFile(wpcliPath, []byte("wp-cli"), 0644); err != nil {
		t.Fatal(err)
	}
	wpcli := &WPCLI{Runner: runner, Path: wpcliPath}
	site := model.Site{ID: "network-example", Domain: "network.example.com", Kind: model.WordPress, PHPVersion: "8.4", WordPressMultisite: model.MultisiteSubdomains}
	if err := wpcli.Ensure(context.Background(), site, Identity{Name: "wpxsite", UID: os.Getuid(), GID: os.Getgid()}, filepath.Join(root, "public"), DatabaseCredentials{Name: "wordpress", User: "wordpress", Password: "password", Host: "localhost"}); err != nil {
		t.Fatal(err)
	}
	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, strings.Join(call, " "))
	}
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "core multisite-install") || !strings.Contains(joined, "--subdomains") || strings.Contains(joined, "core install --url") {
		t.Fatalf("unexpected WordPress installation commands: %s", joined)
	}
}

func TestProtectWordPressConfigRejectsSymlinkAndRestrictsFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "wp-config.php")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	identity := Identity{UID: os.Getuid(), GID: os.Getgid()}
	if err := protectWordPressConfig(link, identity); err == nil {
		t.Fatal("symlinked WordPress configuration was accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := protectWordPressConfig(link, identity); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode=%v", info.Mode().Perm())
	}
}

func TestDisableRedisIsIdempotentBeforePluginExists(t *testing.T) {
	runner := &recordRunner{}
	publicDir := t.TempDir()
	wpcli := &WPCLI{Runner: runner, Path: "/usr/local/lib/wpx/wp-cli.phar"}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if err := wpcli.ApplyRedis(context.Background(), site, Identity{Name: "wpxsite"}, publicDir, false); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("absent Redis plugin triggered commands: %#v", runner.calls)
	}
}

func TestDisableRedisRemovesExistingDropInBeforeDeactivation(t *testing.T) {
	runner := &recordRunner{}
	publicDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(publicDir, "wp-content", "plugins", "redis-cache"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "wp-content", "object-cache.php"), []byte("<?php"), 0640); err != nil {
		t.Fatal(err)
	}
	wpcli := &WPCLI{Runner: runner, Path: "/usr/local/lib/wpx/wp-cli.phar"}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
	if err := wpcli.ApplyRedis(context.Background(), site, Identity{Name: "wpxsite"}, publicDir, false); err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		joined = append(joined, strings.Join(call, " "))
	}
	sequence := strings.Join(joined, "\n")
	activate := strings.Index(sequence, "plugin activate redis-cache")
	disable := strings.Index(sequence, "redis disable")
	deactivate := strings.Index(sequence, "plugin deactivate redis-cache")
	if activate < 0 || disable <= activate || deactivate <= disable {
		t.Fatalf("unsafe Redis cleanup order: %s", sequence)
	}
}
