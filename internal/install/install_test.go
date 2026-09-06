package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/config"
)

func TestInstallerRejectsUnsafeSystemdConfigurationPathBeforeHostChanges(t *testing.T) {
	_, err := Run(context.Background(), Options{DryRun: true, ConfigPath: "/etc/wpx/config.json\nExecStart=/tmp/untrusted", Output: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "contain no whitespace") {
		t.Fatalf("error=%v", err)
	}
}

func TestResumePreservesConfigurationAndRotatesOnlyBootstrapToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	statePath := filepath.Join(filepath.Dir(path), "state.db")
	before := config.Default()
	before.ListenAddress = "100.100.100.1:9443"
	before.UpdateChecks = false
	before.WebUID = 234
	before.BootstrapTokenHash = "old bootstrap hash"
	if err := config.Save(path, before); err != nil {
		t.Fatal(err)
	}
	after, token, existing, err := installationConfig(path, statePath, false)
	if err != nil || !existing {
		t.Fatalf("existing=%v err=%v", existing, err)
	}
	digest := sha256.Sum256([]byte(token))
	if token == "" || after.BootstrapTokenHash != hex.EncodeToString(digest[:]) {
		t.Fatal("resumed setup token must match the saved hash")
	}
	after.BootstrapTokenHash = before.BootstrapTokenHash
	if after != before {
		t.Fatalf("resume changed existing configuration: before=%+v after=%+v", before, after)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := installationConfig(path, statePath, false); err == nil {
		t.Fatal("a malformed config must not be replaced with defaults")
	}
}

func TestMissingConfigurationCannotBootstrapOverPersistedState(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		t.Run("state.db"+suffix, func(t *testing.T) {
			root := t.TempDir()
			path, statePath := filepath.Join(root, "config.json"), filepath.Join(root, "state.db")
			evidence := []byte("existing panel state must survive recovery")
			if err := os.WriteFile(statePath+suffix, evidence, 0600); err != nil {
				t.Fatal(err)
			}
			_, token, _, err := installationConfig(path, statePath, false)
			if err == nil || !strings.Contains(err.Error(), "restore the original configuration and secret key") || token != "" {
				t.Fatalf("must reject missing configuration before creating replacement secrets: token=%q err=%v", token, err)
			}
			actual, err := os.ReadFile(statePath + suffix)
			if err != nil || string(actual) != string(evidence) {
				t.Fatalf("recovery evidence changed: %q err=%v", actual, err)
			}
			for _, name := range []string{"config.json", "secret.key"} {
				if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Fatalf("missing %s must not be regenerated: %v", name, err)
				}
			}
		})
	}
}

func TestConfigurationCanBootstrapBeforeAnyPanelStateExists(t *testing.T) {
	root := t.TempDir()
	cfg, token, existing, err := installationConfig(filepath.Join(root, "config.json"), filepath.Join(root, "state.db"), false)
	if err != nil || existing || token == "" || cfg.ListenAddress != "0.0.0.0:9443" {
		t.Fatalf("first-run bootstrap rejected: existing=%v cfg=%+v err=%v", existing, cfg, err)
	}
}

func TestInstallationAddressRespectsPreservedListener(t *testing.T) {
	for _, test := range []struct {
		listener string
		want     string
	}{
		{"127.0.0.1:9443", "your existing domain/Tailscale URL, or https://127.0.0.1:9443/setup through an SSH tunnel (access settings preserved)"},
		{"[::1]:9444", "your existing domain/Tailscale URL, or https://[::1]:9444/setup through an SSH tunnel (access settings preserved)"},
		{"100.100.100.1:9443", "https://100.100.100.1:9443/setup"},
		{"0.0.0.0:9443", "https://SERVER_IP:9443/setup"},
		{"[::]:9444", "https://SERVER_IP:9444/setup"},
	} {
		t.Run(test.listener, func(t *testing.T) {
			cfg := config.Default()
			cfg.ListenAddress = test.listener
			if got := installationPanelAddress(cfg, true); got != test.want {
				t.Fatalf("panel address=%q, want %q", got, test.want)
			}
		})
	}
	initial := config.Default()
	initial.ListenAddress = "0.0.0.0:9443"
	if got := installationPanelAddress(initial, false); got != "https://SERVER_IP:9443/setup" {
		t.Fatalf("fresh install address=%q", got)
	}
}

func TestSecretKeySurvivesResumeAndInvalidKeyIsNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := ensureSecretKey(path, os.Getuid(), os.Getgid(), false); err == nil {
		t.Fatal("an existing installation must not silently replace its lost key")
	}
	if err := ensureSecretKey(path, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSecretKey(path, os.Getuid(), os.Getgid(), false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("encryption key changed on resume: %v", err)
	}
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureSecretKey(path, os.Getuid(), os.Getgid(), true); err == nil {
		t.Fatal("even initial retries must refuse a damaged key")
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != "damaged" {
		t.Fatalf("damaged key was overwritten: %v", err)
	}
}

func TestInstallMarkerSurvivesFailureUntilPanelAndServicesAreReady(t *testing.T) {
	for _, failure := range []string{"restart", "health", "wpx-broker.service", "wpx.service", "nginx.service", "mariadb.service", "redis-server.service", ""} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			paths := installPaths{DataRoot: root, MarkerPath: filepath.Join(root, ".installing")}
			if err := beginInstall(paths); err != nil {
				t.Fatal(err)
			}
			var calls []string
			run := func(_ context.Context, _ string, args ...string) error {
				calls = append(calls, args[0])
				if args[0] == "is-active" && len(args) != 3 {
					t.Fatal("is-active must probe only one unit at a time")
				}
				if failure == args[0] || args[0] == "is-active" && failure == args[2] {
					return errors.New("service unavailable")
				}
				return nil
			}
			health := func(context.Context, config.Config) error {
				calls = append(calls, "health")
				if failure == "health" {
					return errors.New("connection refused")
				}
				return nil
			}
			err := finishInstallation(context.Background(), config.Default(), paths.MarkerPath, run, health)
			if failure != "" {
				if err == nil || !hasInstallMarker(paths.MarkerPath) {
					t.Fatalf("failed %s must remain resumable; err=%v", failure, err)
				}
				return
			}
			if err != nil || hasInstallMarker(paths.MarkerPath) {
				t.Fatalf("ready panel must complete installation; err=%v", err)
			}
			if strings.Join(calls, ",") != "restart,health,is-active,is-active,is-active,is-active,is-active" {
				t.Fatalf("unexpected readiness ordering: %v", calls)
			}
		})
	}
}

func TestLegacyRcloneFailureIsTheOnlyUnmarkedInstallThatCanResume(t *testing.T) {
	root := t.TempDir()
	paths := installPaths{
		ConfigPath:      filepath.Join(root, "etc", "config.json"),
		DataRoot:        filepath.Join(root, "data"),
		InstalledBinary: filepath.Join(root, "bin", "wpx"),
		DependencyRoot:  filepath.Join(root, "lib"),
		NginxRoot:       filepath.Join(root, "nginx"),
		MySQLRoot:       filepath.Join(root, "mysql"),
		MarkerPath:      filepath.Join(root, "data", ".installing"),
	}
	for _, path := range []string{
		paths.InstalledBinary,
		filepath.Join(paths.DependencyRoot, "wp-cli.phar"),
		filepath.Join(paths.DependencyRoot, "restic"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("artifact"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if !isLegacyRcloneFailure(paths) {
		t.Fatal("expected the exact pre-rclone artifact sequence to resume")
	}
	if err := os.WriteFile(filepath.Join(paths.DependencyRoot, "rclone"), []byte("complete"), 0755); err != nil {
		t.Fatal(err)
	}
	if isLegacyRcloneFailure(paths) {
		t.Fatal("a completed rclone dependency must not bypass fresh-host checks")
	}
}

func TestBeginInstallCreatesRecognizableProtectedMarker(t *testing.T) {
	root := t.TempDir()
	paths := installPaths{DataRoot: root, MarkerPath: filepath.Join(root, ".installing")}
	if err := beginInstall(paths); err != nil {
		t.Fatal(err)
	}
	if !hasInstallMarker(paths.MarkerPath) {
		t.Fatal("installation marker is not recognized")
	}
	info, err := os.Stat(paths.MarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("marker mode=%o, want 600", info.Mode().Perm())
	}
}
