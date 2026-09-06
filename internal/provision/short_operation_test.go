//go:build linux

package provision

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type shortOperationIdentity struct{ calls atomic.Int32 }

func (identity *shortOperationIdentity) Ensure(ctx context.Context, site model.Site, directory string) (Identity, error) {
	identity.calls.Add(1)
	return (currentIdentity{}).Ensure(ctx, site, directory)
}

var shortOperations = []struct {
	name string
	run  func(*Host, model.Site) error
}{
	{name: "file save", run: func(host *Host, site model.Site) error {
		return host.WriteFile(context.Background(), site, "note.txt", "saved")
	}},
	{name: "administrator login", run: func(host *Host, site model.Site) error {
		_, _, err := host.MagicLogin(context.Background(), site)
		return err
	}},
	{name: "WordPress command", run: func(host *Host, site model.Site) error {
		return host.SetPlugin(context.Background(), site, "akismet", true)
	}},
}

// The mutex is the same one held by domain changes and deletion. Requests
// already accepted by the broker must wait, then re-check the resulting tree.
func TestShortOperationsWaitForLifecycleLock(t *testing.T) {
	for _, operation := range shortOperations {
		t.Run(operation.name, func(t *testing.T) {
			host := testHost(t, &recordRunner{})
			identity := &shortOperationIdentity{}
			host.Identities = identity
			host.Output = &fakeOutputRunner{}
			site := model.Site{ID: "existing-site", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
			createWordPressPublicFixture(t, host, site)

			host.mu.Lock()
			done := startShortOperation(t, host, site, operation.run)
			callsWhileLocked := identity.calls.Load()
			host.mu.Unlock()
			if callsWhileLocked != 0 {
				t.Fatal("identity creation overlapped lifecycle operation")
			}
			if err := finishShortOperation(t, done); err != nil {
				t.Fatal(err)
			}
			if identity.calls.Load() != 1 {
				t.Fatal("operation did not resume after lifecycle lock was released")
			}
		})
	}
}

func TestShortOperationsCannotRecreateDeletedSiteIdentity(t *testing.T) {
	for _, operation := range shortOperations {
		t.Run(operation.name, func(t *testing.T) {
			host := testHost(t, &recordRunner{})
			identity := &shortOperationIdentity{}
			output := &fakeOutputRunner{}
			host.Identities, host.Output = identity, output
			site := model.Site{ID: "deleted-site", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
			publicDir := createWordPressPublicFixture(t, host, site)

			host.mu.Lock()
			done := startShortOperation(t, host, site, operation.run)
			// Model deletion completing while an already-authorized request waits.
			removeErr := os.RemoveAll(filepath.Dir(publicDir))
			host.mu.Unlock()
			if removeErr != nil {
				t.Fatal(removeErr)
			}
			if err := finishShortOperation(t, done); err == nil {
				t.Fatal("operation accepted a deleted site")
			}
			if identity.calls.Load() != 0 || len(output.calls) != 0 {
				t.Fatal("deleted site reached identity creation or WordPress command runner")
			}
			if _, err := os.Lstat(filepath.Dir(publicDir)); !os.IsNotExist(err) {
				t.Fatalf("deleted site directory was recreated: %v", err)
			}
		})
	}
}

func TestShortOperationsRejectSymlinkBeforeIdentityCreation(t *testing.T) {
	for _, operation := range shortOperations {
		for _, component := range []string{"site", "public"} {
			t.Run(operation.name+"/"+component, func(t *testing.T) {
				host := testHost(t, &recordRunner{})
				identity := &shortOperationIdentity{}
				output := &fakeOutputRunner{}
				host.Identities, host.Output = identity, output
				site := model.Site{ID: "linked-site", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}
				publicDir := createWordPressPublicFixture(t, host, site)
				linkedDir := publicDir
				if component == "site" {
					linkedDir = filepath.Dir(publicDir)
				}
				movedDir := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(linkedDir, movedDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(movedDir, linkedDir); err != nil {
					t.Fatal(err)
				}
				if err := operation.run(host, site); err == nil {
					t.Fatal("operation accepted a symlinked site directory")
				}
				if identity.calls.Load() != 0 || len(output.calls) != 0 {
					t.Fatal("symlinked site reached identity creation or WordPress command runner")
				}
			})
		}
	}
}

func startShortOperation(t *testing.T, host *Host, site model.Site, run func(*Host, model.Site) error) <-chan error {
	t.Helper()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- run(host, site)
	}()
	<-started
	select {
	case err := <-done:
		// Release before failing so cleanup cannot strand another waiter.
		host.mu.Unlock()
		t.Fatalf("operation completed while lifecycle lock was held: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	return done
}

func finishShortOperation(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not resume after lifecycle lock was released")
		return nil
	}
}
