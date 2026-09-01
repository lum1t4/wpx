package provision

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type restoreInputRunner struct {
	data  []byte
	calls [][]string
}

func (r *restoreInputRunner) RunInput(_ context.Context, input io.Reader, executable string, args ...string) error {
	r.calls = append(r.calls, append([]string{executable}, args...))
	data, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	r.data = data
	return nil
}

func TestWordPressRestoreTestImportsDisposableDatabaseWithoutTouchingLiveSite(t *testing.T) {
	runner := &stagingRunner{}
	host := testHost(t, runner)
	restic := filepath.Join(host.DataRoot, "restic")
	if err := os.MkdirAll(filepath.Dir(restic), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	host.ResticPath = restic
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active", Environment: "production"}
	livePublic := filepath.Join(host.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(livePublic, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(livePublic, "sentinel.txt"), []byte("live"), 0640); err != nil {
		t.Fatal(err)
	}
	host.Environment = &backupEnvironmentRunner{restoredPublic: livePublic, restoredDatabase: true}
	input := &restoreInputRunner{}
	host.Input = input
	snapshotID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := host.TestRestore(context.Background(), site, testBackupTarget(), snapshotID, "restore-test-job"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input.data, []byte("database")) || len(input.calls) != 1 || !slices.Contains(input.calls[0], "wpx_restore_test_98032fd93ab91c30") {
		t.Fatalf("disposable import=%q calls=%#v", input.data, input.calls)
	}
	if content, err := os.ReadFile(filepath.Join(livePublic, "sentinel.txt")); err != nil || string(content) != "live" {
		t.Fatalf("live content changed: %q err=%v", content, err)
	}
	before := len(runner.calls)
	if err := host.TestRestore(context.Background(), site, testBackupTarget(), snapshotID, "restore-test-job"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != before {
		t.Fatal("completed restore test replayed external work")
	}
}
