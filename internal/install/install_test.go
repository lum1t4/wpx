package install

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerRejectsUnsafeSystemdConfigurationPathBeforeHostChanges(t *testing.T) {
	_, err := Run(context.Background(), Options{DryRun: true, ConfigPath: "/etc/wpx/config.json\nExecStart=/tmp/untrusted", Output: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "contain no whitespace") {
		t.Fatalf("error=%v", err)
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
