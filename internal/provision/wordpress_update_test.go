package provision

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestWordPressUpdateCreatesRecoveryAndRollsBackDatabaseOnFailure(t *testing.T) {
	runner := &stagingRunner{}
	host := testHost(t, runner)
	wpcli := filepath.Join(host.DataRoot, "wp-cli.phar")
	restic := filepath.Join(host.DataRoot, "restic")
	if err := os.MkdirAll(filepath.Dir(wpcli), 0750); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{wpcli, restic} {
		if err := os.WriteFile(path, []byte("test"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	host.WordPress = &WPCLI{Runner: runner, Path: wpcli}
	host.ResticPath = restic
	host.Environment = &backupEnvironmentRunner{}
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active", Environment: "production"}
	publicDir := filepath.Join(host.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(publicDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(host.SiteRoot, site.ID, "tmp"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "wp-config.php"), []byte("production-config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "index.php"), []byte("production"), 0640); err != nil {
		t.Fatal(err)
	}
	recoveryID, err := host.UpdateWordPress(context.Background(), site, testBackupTarget(), model.WordPressUpdate{Component: model.WordPressPlugin, Name: "akismet"}, "update-job")
	if err != nil || recoveryID == "" {
		t.Fatalf("recovery=%q err=%v", recoveryID, err)
	}
	foundUpdate := false
	for _, call := range runner.calls {
		if slices.Contains(call, "akismet") && slices.Contains(call, "update") {
			foundUpdate = true
		}
	}
	if !foundUpdate {
		t.Fatalf("plugin update command missing: %#v", runner.calls)
	}

	runner.failContaining = "plugin update broken-plugin"
	before := len(runner.calls)
	_, err = host.UpdateWordPress(context.Background(), site, testBackupTarget(), model.WordPressUpdate{Component: model.WordPressPlugin, Name: "broken-plugin"}, "failed-update-job")
	if err == nil {
		t.Fatal("failed plugin update reported success")
	}
	rolledBack := false
	for _, call := range runner.calls[before:] {
		if slices.Contains(call, "import") && strings.Contains(strings.Join(call, " "), "database.sql") {
			rolledBack = true
		}
	}
	if !rolledBack {
		t.Fatalf("database rollback was not attempted: %#v", runner.calls[before:])
	}
}
