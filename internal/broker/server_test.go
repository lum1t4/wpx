//go:build linux

package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func TestBrokerCreatesOnlyValidatedSiteRoot(t *testing.T) {
	dir := t.TempDir()
	server := &Server{
		SocketPath: filepath.Join(dir, "run", "broker.sock"),
		SiteRoot:   filepath.Join(dir, "sites"),
		AllowedUID: uint32(os.Getuid()),
		SocketGID:  -1,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(server.SocketPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	client := Client{SocketPath: server.SocketPath}
	var result EnsureSiteRootResult
	err := client.Call(ctx, OpEnsureSiteRoot, "create-example", EnsureSiteRootRequest{SiteID: "example-com"}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("first operation did not report creation")
	}
	if st, err := os.Stat(filepath.Join(server.SiteRoot, "example-com")); err != nil || !st.IsDir() {
		t.Fatalf("site directory missing: %v", err)
	}
	err = client.Call(ctx, OpEnsureSiteRoot, "escape", EnsureSiteRootRequest{SiteID: "../../root"}, nil)
	if err == nil {
		t.Fatal("path traversal site id was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "root")); !os.IsNotExist(err) {
		t.Fatalf("escape path was touched: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop")
	}
}

func TestPHPVersionChangeRejectsUnauthorizedBrokerPeer(t *testing.T) {
	dir := t.TempDir()
	manager := &phpVersionManager{}
	server := &Server{
		SocketPath: filepath.Join(dir, "run", "broker.sock"), SiteRoot: filepath.Join(dir, "sites"),
		AllowedUID: uint32(os.Getuid()) + 1, SocketGID: -1, Provisioner: manager,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	for range 100 {
		if _, err := os.Stat(server.SocketPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	payload := ChangePHPVersionRequest{
		Site:   model.Site{ID: "php-site", Domain: "example.com", Kind: model.PHP, PHPVersion: "8.4", Status: "php_changing"},
		Change: model.PHPVersionChange{PreviousVersion: "8.4", Version: "8.5"},
	}
	client := Client{SocketPath: server.SocketPath}
	if err := client.Call(ctx, OpChangePHPVersion, "php-version-peer-test", payload, nil); err == nil {
		t.Fatal("an unauthorized peer changed the PHP version")
	}
	if manager.calls != 0 {
		t.Fatal("unauthorized request reached the privileged operation")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker did not stop")
	}
}
