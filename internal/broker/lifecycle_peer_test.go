//go:build linux

package broker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleBrokerRejectsUnauthorizedPeer(t *testing.T) {
	for _, operation := range []Operation{OpChangeDomain, OpDeleteSite} {
		t.Run(string(operation), func(t *testing.T) {
			dir := t.TempDir()
			manager := &lifecycleManager{}
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
			var payload any = domainRequest(t)
			if operation == OpDeleteSite {
				site := domainRequest(t).Site
				site.Status = "deleting"
				payload = DeleteSiteRequest{Site: site}
			}
			client := Client{SocketPath: server.SocketPath}
			if err := client.Call(ctx, operation, "durable-site-job", payload, nil); err == nil {
				t.Fatal("unauthorized peer changed a site")
			}
			if manager.domainCalls != 0 || manager.deleteCalls != 0 {
				t.Fatal("unauthorized request reached a privileged lifecycle operation")
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
		})
	}
}
