//go:build linux

package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

func TestSiteBackupsShowsDurableRunStateAndResult(t *testing.T) {
	server, owner, _ := navigationServer(t)
	ctx := context.Background()
	navigationSite(t, server, owner, model.Site{ID: "backup-history-site", Domain: "backups.example.com", Kind: model.Static})
	target, _, err := server.store.CreateS3Target(ctx, owner, model.BackupTarget{Name: "History storage", Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := server.store.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim storage job: %#v %v %v", job, found, err)
	}
	if err := server.store.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	jobID, err := server.store.EnqueueSiteBackup(ctx, owner, "backup-history-site", target.ID)
	if err != nil {
		t.Fatal(err)
	}

	page := navigationRequest(t, server, owner, http.MethodGet, "/sites/backup-history-site/backups", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	body := page.Body.String()
	if !strings.Contains(body, "Backup history") || !strings.Contains(body, "Queued") || !strings.Contains(body, `href="/jobs#job-`+jobID+`"`) || strings.Contains(body, "Restore point ready") {
		t.Fatalf("queued backup history is inaccurate: %s", body)
	}

	job, found, err = server.store.ClaimNextJob(ctx)
	if err != nil || !found || job.ID != jobID {
		t.Fatalf("claim backup job: %#v %v %v", job, found, err)
	}
	snapshotID := strings.Repeat("b", 64)
	if err := server.store.FinishJob(ctx, job, `{"snapshot_id":"`+snapshotID+`"}`, nil); err != nil {
		t.Fatal(err)
	}
	page = navigationRequest(t, server, owner, http.MethodGet, "/sites/backup-history-site/backups", nil)
	requireNavigationStatus(t, page, http.StatusOK)
	body = page.Body.String()
	if !strings.Contains(body, "Success") || !strings.Contains(body, `title="`+snapshotID+`"`) || !strings.Contains(body, snapshotID[:12]+"…") || strings.Contains(body, ">"+snapshotID+"</code>") || !strings.Contains(body, "Restore point ready") || !strings.Contains(body, " UTC</time>") {
		t.Fatalf("completed backup result missing from history: %s", body)
	}
}

func TestBackupHistoryTimeUsesCompactUTCAndPreservesInvalidLegacyValue(t *testing.T) {
	if got := backupHistoryTime("2026-09-08T10:01:05.123456789+02:00"); got != "2026-09-08 08:01 UTC" {
		t.Fatalf("compact time = %q", got)
	}
	if got := backupHistoryTime("legacy-time"); got != "legacy-time" {
		t.Fatalf("legacy time = %q", got)
	}
}
