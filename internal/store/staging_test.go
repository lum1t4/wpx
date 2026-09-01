package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/rbac"
)

func TestCreateStagingLinksEnvironmentAndCopiesGrants(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{5}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", WordPressMultisite: model.MultisiteSubdirectories})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim production job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	collaborator, err := state.CreateUser(ctx, owner, "developer", "another-secure-password", rbac.Collaborator, []string{"example-com"})
	if err != nil {
		t.Fatal(err)
	}
	_, password, err := state.CreateStaging(ctx, collaborator, "example-com", "staging-example", "staging.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(password) < 24 {
		t.Fatal("generated staging password is too short")
	}
	username, storedPassword, err := state.StagingCredential(ctx, "staging-example")
	if err != nil || username != "wpx" || storedPassword != password {
		t.Fatalf("staging credential did not round-trip: username=%q err=%v", username, err)
	}
	var ciphertext []byte
	if err := state.db.QueryRowContext(ctx, `SELECT password_ciphertext FROM staging_credentials WHERE site_id=?`, "staging-example").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(password)) {
		t.Fatal("staging password was stored as plaintext")
	}
	staging, err := state.Site(ctx, "staging-example")
	if err != nil || staging.Environment != "staging" || staging.ParentSiteID != "example-com" || staging.WordPressMultisite != model.MultisiteSubdirectories {
		t.Fatalf("staging=%#v err=%v", staging, err)
	}
	if !state.UserCanSite(ctx, collaborator, staging.ID, rbac.DeploySite) {
		t.Fatal("source collaborator grant was not copied to staging")
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "wordpress.staging_create" || job.TargetID != staging.ID {
		t.Fatalf("job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueStagingSync(ctx, collaborator, staging.ID); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "wordpress.staging_sync" {
		t.Fatalf("sync job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Deploy recovery", Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1",
		BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "backup.target_init" {
		t.Fatalf("target job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.EnqueueStagingDeploy(ctx, collaborator, staging.ID, target.ID, model.StagingSelection{Full: true}); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "wordpress.staging_deploy" || job.TargetID != "example-com" {
		t.Fatalf("deploy job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, `{"recovery_snapshot_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`, nil); err != nil {
		t.Fatal(err)
	}
	snapshots, err := state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("production recovery snapshots=%#v err=%v", snapshots, err)
	}
}

func TestCreateStagingCopiesOnlyActiveOwnedApexDNS(t *testing.T) {
	state := openTestStore(t)
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{6}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "dns-source", Domain: "example.net", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := state.ClaimNextJob(ctx)
	if err != nil || !found {
		t.Fatalf("claim site job: found=%v err=%v", found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	provider, err := state.CreateDNSProvider(ctx, owner, model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.provider_verify" {
		t.Fatalf("provider job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, "{}", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateDNSRecord(ctx, owner, model.DNSRecord{SiteID: "dns-source", ProviderID: provider.ID, Name: "example.net", Type: "A", Value: "192.0.2.44", TTL: 300, Proxied: true}); err != nil {
		t.Fatal(err)
	}
	job, found, err = state.ClaimNextJob(ctx)
	if err != nil || !found || job.Kind != "dns.record_apply" {
		t.Fatalf("record job=%#v found=%v err=%v", job, found, err)
	}
	if err := state.FinishJob(ctx, job, `{"remote_id":"owned-apex"}`, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.CreateStaging(ctx, owner, "dns-source", "dns-stage", "stage.example.net"); err != nil {
		t.Fatal(err)
	}
	records, err := state.ListSiteDNSRecords(ctx, "dns-stage")
	if err != nil || len(records) != 1 {
		t.Fatalf("staging records=%#v err=%v", records, err)
	}
	record := records[0]
	if record.Name != "stage.example.net" || record.Type != "A" || record.Value != "192.0.2.44" || record.TTL != 300 || !record.Proxied || record.Status != "queued" {
		t.Fatalf("copied record=%#v", record)
	}
	var dnsJobs, stagingJobs int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='dns.record_apply' AND target_id=? AND status='queued'`, record.ID).Scan(&dnsJobs); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='wordpress.staging_create' AND target_id=? AND status='queued'`, "dns-stage").Scan(&stagingJobs); err != nil {
		t.Fatal(err)
	}
	if dnsJobs != 1 || stagingJobs != 1 {
		t.Fatalf("dns jobs=%d staging jobs=%d", dnsJobs, stagingJobs)
	}
	for {
		job, found, err = state.ClaimNextJob(ctx)
		if err != nil || !found {
			t.Fatalf("claim staging dependency: found=%v err=%v", found, err)
		}
		result := "{}"
		if job.Kind == "dns.record_apply" {
			result = `{"remote_id":"staging-record"}`
		}
		if err := state.FinishJob(ctx, job, result, nil); err != nil {
			t.Fatal(err)
		}
		if job.Kind == "wordpress.staging_create" {
			break
		}
	}
	var automaticCertificates int
	if err := state.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind='site.certificate_dns' AND target_id='dns-stage' AND status='queued'`).Scan(&automaticCertificates); err != nil {
		t.Fatal(err)
	}
	if automaticCertificates != 1 {
		t.Fatalf("automatic certificate jobs=%d", automaticCertificates)
	}
}
