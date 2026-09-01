package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestRestoreClonePreservesWordPressCredentialsAndProtectsStaging(t *testing.T) {
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
	host.Database = fakeDatabase{}
	host.WordPress = &WPCLI{Runner: runner, Path: wpcli}
	host.ResticPath = restic
	source := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active", Environment: "production"}
	target := model.Site{ID: "restored-stage", Domain: "restored.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "queued", Environment: "staging", ParentSiteID: source.ID}
	sourcePublic := filepath.Join(host.SiteRoot, source.ID, "public")
	host.Environment = &backupEnvironmentRunner{restoredPublic: sourcePublic, restoredDatabase: true}
	password := "a-long-random-staging-password"
	if err := host.RestoreClone(context.Background(), source, target, testBackupTarget(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "wpx", password, "restore-clone-job"); err != nil {
		t.Fatal(err)
	}
	targetPublic := filepath.Join(host.SiteRoot, target.ID, "public")
	configuration, err := os.ReadFile(filepath.Join(targetPublic, "wp-config.php"))
	if err != nil || !strings.Contains(string(configuration), "restored-stage") {
		t.Fatalf("destination configuration was not preserved: %q err=%v", configuration, err)
	}
	if _, err := os.Stat(filepath.Join(targetPublic, "wp-content", "mu-plugins", "wpx-staging.php")); err != nil {
		t.Fatalf("staging guard missing: %v", err)
	}
	htpasswd, err := os.ReadFile(filepath.Join(host.StagingAuthRoot, target.ID+".htpasswd"))
	if err != nil || bytesContain(htpasswd, []byte(password)) || !strings.HasPrefix(string(htpasswd), "wpx:$2y$") {
		t.Fatalf("staging HTTP credential was not safely installed: %q err=%v", htpasswd, err)
	}
	if content, err := os.ReadFile(filepath.Join(targetPublic, "index.html")); err != nil || string(content) != "restored" {
		t.Fatalf("restored content=%q err=%v", content, err)
	}
	// Completion markers let a lost broker response retry without provisioning
	// or importing the destination a second time.
	before := len(runner.calls)
	if err := host.RestoreClone(context.Background(), source, target, testBackupTarget(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "wpx", password, "restore-clone-job"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != before {
		t.Fatalf("completed restore clone unexpectedly replayed host changes: before=%d after=%d", before, len(runner.calls))
	}
}

func bytesContain(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}
