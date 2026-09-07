package store

import (
	"context"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestHostingStateKeepsPasswordHashedAndRuntimeBoundToUpstream(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner, err := s.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "node-app", Domain: "node.example.com", Kind: model.ReverseProxy, Upstream: "http://127.0.0.1:3100"}
	if _, err := s.CreateSite(ctx, owner, site); err != nil {
		t.Fatal(err)
	}
	job, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim site job: %v", err)
	}
	if err := s.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	site.Status = "active"
	runtime := model.NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Port: 3100, NodeVersion: "24.20.0"}
	if _, err := s.ConfigureNodeRuntime(ctx, owner, site, runtime); err != nil {
		t.Fatal(err)
	}
	runtimeJob, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found || runtimeJob.Kind != "hosting.node_apply" {
		t.Fatalf("claim runtime job: %#v %v", runtimeJob, err)
	}
	if err := s.RetryHostingJob(ctx, runtimeJob.ID, "broker response lost"); err != nil {
		t.Fatal(err)
	}
	retry, found, err := s.ClaimNextJob(ctx)
	if err != nil || !found || retry.IdempotencyKey != runtimeJob.IdempotencyKey {
		t.Fatal("hosting retry changed its broker key")
	}
	if err := s.FinishJob(ctx, retry, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigureNodeRuntime(ctx, owner, site, model.NodeRuntime{SiteID: site.ID, Entrypoint: "server.js", Port: 3200, NodeVersion: "24.20.0"}); err == nil {
		t.Fatal("runtime port diverged from persisted upstream")
	}
	ftp, _, err := s.CreateFTPUser(ctx, owner, site.ID, "deploy", "a-long-ftp-password")
	if err != nil {
		t.Fatal(err)
	}
	if ftp.PasswordHash != "" {
		t.Fatal("returned FTP password hash")
	}
	stored, err := s.FTPUser(ctx, ftp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == "a-long-ftp-password" || !strings.HasPrefix(stored.PasswordHash, "$2") {
		t.Fatal("FTP password was not bcrypt hashed")
	}
}
