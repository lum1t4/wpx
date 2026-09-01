//go:build linux

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

func TestFileManagerWritesAtomicallyLintsPHPAndKeepsRevision(t *testing.T) {
	runner := &recordRunner{}
	host := testHost(t, runner)
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4"}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.SiteRoot, site.ID, "public", "index.php")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.WriteFile(context.Background(), site, "index.php", "<?php echo 'changed';\n"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "<?php echo 'changed';\n" {
		t.Fatalf("unexpected saved file %q: %v", after, err)
	}
	lastCall := strings.Join(runner.calls[len(runner.calls)-1], " ")
	if !strings.Contains(lastCall, "/usr/bin/php8.4 -l") {
		t.Fatalf("PHP lint was not used: %s", lastCall)
	}
	var revisionFound bool
	err = filepath.Walk(filepath.Join(host.DataRoot, "revisions", site.ID), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			content, readErr := os.ReadFile(path)
			if readErr == nil && string(content) == string(before) {
				revisionFound = true
			}
		}
		return err
	})
	if err != nil || !revisionFound {
		t.Fatalf("previous content was not revisioned: %v", err)
	}
}

type lintFailureRunner struct{}

func (lintFailureRunner) Run(_ context.Context, executable string, _ ...string) error {
	if executable == "/usr/sbin/runuser" {
		return errors.New("syntax error")
	}
	return nil
}

func TestFileManagerRejectsTraversalSymlinksAndFailedLint(t *testing.T) {
	host := testHost(t, lintFailureRunner{})
	site := model.Site{ID: "example-com", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4"}
	if err := host.Provision(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.SiteRoot, site.ID, "public", "index.php")
	before, _ := os.ReadFile(path)
	if err := host.WriteFile(context.Background(), site, "index.php", "<?php broken"); err == nil {
		t.Fatal("failed PHP lint was accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("failed lint changed the live file")
	}
	if err := host.WriteFile(context.Background(), site, "../../outside.php", "bad"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(host.SiteRoot, site.ID, "public", "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := host.ReadFile(context.Background(), site, "link.txt"); err == nil {
		t.Fatal("symlinked file was read")
	}
}
