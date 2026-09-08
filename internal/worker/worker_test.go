package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"github.com/lum1t4/wpx/internal/store"
)

type fakeProvisioner struct {
	sites                []model.Site
	dnsProviders         []model.DNSProvider
	wildcards            []bool
	err                  error
	backupCalls          []string
	backupErrors         []error
	backupResult         broker.BackupSiteResult
	searchReplaceCalls   []string
	searchReplaceErrors  []error
	searchReplaceResults []broker.WordPressSearchReplaceResult
}

type fakeDNS struct{}

func (fakeDNS) Verify(context.Context, model.DNSProvider) error { return nil }
func (fakeDNS) Apply(context.Context, model.DNSProvider, model.DNSRecord) (string, error) {
	return "remote-record", nil
}
func (fakeDNS) Delete(context.Context, model.DNSProvider, model.DNSRecord) error { return nil }

func (f *fakeProvisioner) Provision(_ context.Context, site model.Site, _ string) error {
	f.sites = append(f.sites, site)
	return f.err
}

func (f *fakeProvisioner) Disable(_ context.Context, site model.Site, _ bool, _ string) error {
	f.sites = append(f.sites, site)
	return f.err
}

func (f *fakeProvisioner) ApplySnippets(_ context.Context, site model.Site, _ model.SiteSnippets, _ string) error {
	f.sites = append(f.sites, site)
	return f.err
}

func (f *fakeProvisioner) IssueCertificate(_ context.Context, site model.Site, _ string) error {
	f.sites = append(f.sites, site)
	return f.err
}

func (f *fakeProvisioner) IssueDNSCertificate(_ context.Context, site model.Site, provider model.DNSProvider, wildcard bool, _ string) error {
	f.sites = append(f.sites, site)
	f.dnsProviders = append(f.dnsProviders, provider)
	f.wildcards = append(f.wildcards, wildcard)
	return f.err
}

func (f *fakeProvisioner) InitBackup(context.Context, model.BackupTarget, string) error {
	return f.err
}

func (f *fakeProvisioner) BackupSite(_ context.Context, _ model.Site, _ model.BackupTarget, _ model.BackupRetention, idempotencyKey string) (broker.BackupSiteResult, error) {
	f.backupCalls = append(f.backupCalls, idempotencyKey)
	result := f.backupResult
	if result.SnapshotID == "" {
		result.SnapshotID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	if len(f.backupErrors) > 0 {
		err := f.backupErrors[0]
		f.backupErrors = f.backupErrors[1:]
		return result, err
	}
	return result, f.err
}

func (f *fakeProvisioner) SearchReplaceWordPress(_ context.Context, _ model.Site, _ model.BackupTarget, _ model.WordPressSearchReplace, _ bool, idempotencyKey string) (broker.WordPressSearchReplaceResult, error) {
	f.searchReplaceCalls = append(f.searchReplaceCalls, idempotencyKey)
	result := broker.WordPressSearchReplaceResult{RecoverySnapshotID: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}
	if len(f.searchReplaceResults) > 0 {
		result = f.searchReplaceResults[0]
		f.searchReplaceResults = f.searchReplaceResults[1:]
	}
	if len(f.searchReplaceErrors) > 0 {
		err := f.searchReplaceErrors[0]
		f.searchReplaceErrors = f.searchReplaceErrors[1:]
		return result, err
	}
	return result, f.err
}

func (f *fakeProvisioner) RestoreSite(context.Context, model.Site, model.BackupTarget, string, string) (string, error) {
	return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", f.err
}

func (f *fakeProvisioner) RestoreClone(context.Context, model.Site, model.Site, model.BackupTarget, string, string, string, string) error {
	return f.err
}

func (f *fakeProvisioner) TestRestore(context.Context, model.Site, model.BackupTarget, string, string) error {
	return f.err
}

func (f *fakeProvisioner) CreateStaging(_ context.Context, _ model.Site, target model.Site, _, _, _ string) error {
	f.sites = append(f.sites, target)
	return f.err
}

func (f *fakeProvisioner) DeployStaging(context.Context, model.Site, model.Site, model.BackupTarget, model.StagingSelection, string) (string, error) {
	return "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", f.err
}

func (f *fakeProvisioner) ApplyPerformance(_ context.Context, site model.Site, _ string) error {
	f.sites = append(f.sites, site)
	return f.err
}

func (f *fakeProvisioner) UpdateWordPress(context.Context, model.Site, model.BackupTarget, model.WordPressUpdate, string) (string, error) {
	return "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", f.err
}

func (f *fakeProvisioner) CreateDatabase(context.Context, model.Database, string) error { return f.err }
func (f *fakeProvisioner) DeleteDatabase(context.Context, model.Database, string) error { return f.err }
func (f *fakeProvisioner) InstallDatabaseAdmin(context.Context, string) error           { return f.err }

func TestWorkerCompletesProvisionJob(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &fakeProvisioner{}
	w := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	processed, err := w.ProcessOne(ctx)
	if err != nil || !processed || len(provisioner.sites) != 1 {
		t.Fatalf("processed=%v calls=%d err=%v", processed, len(provisioner.sites), err)
	}
	job, err := state.Job(ctx, jobID)
	if err != nil || job.Status != "succeeded" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}

func TestWorkerAppliesWordPressPerformanceJob(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	provisioner := &fakeProvisioner{}
	worker := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	provisioner.sites = nil
	jobID, err := state.SetWordPressPerformance(ctx, owner, "example-com", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := state.Job(ctx, jobID)
	if job.Status != "succeeded" || len(provisioner.sites) != 1 || provisioner.sites[0].RedisEnabled || !provisioner.sites[0].FastCGICacheEnabled {
		t.Fatalf("job=%#v calls=%#v", job, provisioner.sites)
	}
}

func TestWorkerRecordsWordPressUpdateRecoverySnapshot(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{Name: "Storage", Endpoint: "https://objects.example.com", Bucket: "wpx", Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		if _, err := worker.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
	}
	jobID, err := state.EnqueueWordPressUpdate(ctx, owner, "example-com", target.ID, model.WordPressUpdate{Component: model.WordPressTheme, Name: "twentytwentyfive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := state.Job(ctx, jobID)
	snapshots, err := state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || job.Status != "succeeded" || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Fatalf("job=%#v snapshots=%#v err=%v", job, snapshots, err)
	}
}

func TestWorkerRecordsFailure(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	jobID, _ := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	w := Worker{Store: state, Provisioner: &fakeProvisioner{err: errors.New("host failed")}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := state.Job(ctx, jobID)
	site, _ := state.Site(ctx, "example-com")
	if job.Status != "failed" || job.Error != "host failed" || site.Status != "failed" {
		t.Fatalf("job=%#v site=%#v", job, site)
	}
}

func TestInterruptedJobIsRequeued(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	jobID, _ := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if _, found, err := state.ClaimNextJob(ctx); err != nil || !found {
		t.Fatalf("claim failed: %v", err)
	}
	if recovered, err := state.RequeueInterruptedJobs(ctx); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	job, err := state.Job(ctx, jobID)
	if err != nil || job.Status != "queued" || job.Phase != "waiting" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}

func TestCertificateFailureDoesNotFailSite(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	owner, _ := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	_, _ = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	provisioner := &fakeProvisioner{}
	worker := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	jobID, err := state.EnqueueCertificate(ctx, owner, "example-com")
	if err != nil {
		t.Fatal(err)
	}
	provisioner.err = errors.New("dns is not ready")
	if _, err := worker.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	job, _ := state.Job(ctx, jobID)
	site, _ := state.Site(ctx, "example-com")
	if job.Status != "failed" || site.Status != "active" || site.TLSStatus != "failed" {
		t.Fatalf("job=%#v site=%#v", job, site)
	}
}

func TestWorkerRecordsSiteBackupSnapshot(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Object storage", Endpoint: "https://objects.example.com", Bucket: "backups",
		Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret",
		RepositoryPassword: "a-repository-password-long-enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: state, Provisioner: &fakeProvisioner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("initialize target: processed=%v err=%v", processed, err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("provision site: processed=%v err=%v", processed, err)
	}
	if _, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID); err != nil {
		t.Fatal(err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("back up site: processed=%v err=%v", processed, err)
	}
	snapshots, err := state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
	restoreJobID, err := state.EnqueueSiteRestore(ctx, owner, "example-com", snapshots[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("restore site: processed=%v err=%v", processed, err)
	}
	restoreJob, err := state.Job(ctx, restoreJobID)
	if err != nil || restoreJob.Status != "succeeded" || !strings.Contains(restoreJob.ResultJSON, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatalf("restore job=%#v err=%v", restoreJob, err)
	}
}

func TestWorkerRetriesUncertainBackupWithSameIdempotencyKey(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{8}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Object storage", Endpoint: "https://objects.example.com", Bucket: "backups",
		Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret",
		RepositoryPassword: "a-repository-password-long-enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	provisioner := &fakeProvisioner{}
	w := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, setup := range []string{"initialize target", "provision site"} {
		if processed, err := w.ProcessOne(ctx); err != nil || !processed {
			t.Fatalf("%s: processed=%v err=%v", setup, processed, err)
		}
	}
	jobID, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	provisioner.backupErrors = []error{broker.ErrOutcomeUnknown, nil}
	if processed, err := w.ProcessOne(ctx); err == nil || processed {
		t.Fatalf("lost reply: processed=%v err=%v", processed, err)
	}
	retry, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "queued" || retry.ID != before.ID || retry.IdempotencyKey != before.IdempotencyKey {
		t.Fatalf("retry changed backup identity or state: before=%#v retry=%#v", before, retry)
	}
	if snapshots, err := state.ListSiteSnapshots(ctx, "example-com"); err != nil || len(snapshots) != 0 {
		t.Fatalf("snapshots after uncertain result=%#v err=%v", snapshots, err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("reconcile backup: processed=%v err=%v", processed, err)
	}
	finished, err := state.Job(ctx, jobID)
	if err != nil || finished.Status != "succeeded" {
		t.Fatalf("finished job=%#v err=%v", finished, err)
	}
	if len(provisioner.backupCalls) != 2 || provisioner.backupCalls[0] != before.IdempotencyKey || provisioner.backupCalls[1] != before.IdempotencyKey {
		t.Fatalf("backup idempotency keys=%q want two calls with %q", provisioner.backupCalls, before.IdempotencyKey)
	}
	if snapshots, err := state.ListSiteSnapshots(ctx, "example-com"); err != nil || len(snapshots) != 1 {
		t.Fatalf("recorded snapshots=%#v err=%v", snapshots, err)
	}
}

func TestWorkerRejectsInvalidBackupSnapshotID(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Object storage", Endpoint: "https://objects.example.com", Bucket: "backups",
		Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret",
		RepositoryPassword: "a-repository-password-long-enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}); err != nil {
		t.Fatal(err)
	}
	provisioner := &fakeProvisioner{backupResult: broker.BackupSiteResult{SnapshotID: "not-a-restic-snapshot"}}
	w := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		if processed, err := w.ProcessOne(ctx); err != nil || !processed {
			t.Fatalf("setup: processed=%v err=%v", processed, err)
		}
	}
	jobID, err := state.EnqueueSiteBackup(ctx, owner, "example-com", target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("invalid result: processed=%v err=%v", processed, err)
	}
	job, err := state.Job(ctx, jobID)
	if err != nil || job.Status != "failed" || !strings.Contains(job.Error, "snapshot ID is invalid") {
		t.Fatalf("job=%#v err=%v", job, err)
	}
	if snapshots, err := state.ListSiteSnapshots(ctx, "example-com"); err != nil || len(snapshots) != 0 {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
}

func TestWorkerReconcilesUncertainSearchReplaceAndTerminatesKnownFailure(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{10}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4"}); err != nil {
		t.Fatal(err)
	}
	target, _, err := state.CreateS3Target(ctx, owner, model.BackupTarget{
		Name: "Object storage", Endpoint: "https://objects.example.com", Bucket: "backups",
		Region: "us-east-1", BucketLookup: "path", AccessKey: "access", SecretKey: "secret",
		RepositoryPassword: "a-repository-password-long-enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	provisioner := &fakeProvisioner{}
	w := Worker{Store: state, Provisioner: provisioner, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for range 2 {
		if processed, err := w.ProcessOne(ctx); err != nil || !processed {
			t.Fatalf("setup: processed=%v err=%v", processed, err)
		}
	}
	change := model.WordPressSearchReplace{Search: "private-old.example", Replace: "private-new.example"}
	preview := broker.WordPressSearchReplaceResult{}
	token, err := state.CreateWordPressSearchReplacePreview(ctx, owner, "example-com", target.ID, change, preview)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := state.EnqueueWordPressSearchReplace(ctx, owner, "example-com", target.ID, change, token)
	if err != nil {
		t.Fatal(err)
	}
	before, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	provisioner.searchReplaceErrors = []error{broker.ErrOutcomeUnknown, nil}
	if processed, err := w.ProcessOne(ctx); err == nil || processed {
		t.Fatalf("lost reply: processed=%v err=%v", processed, err)
	}
	retry, err := state.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Status != "queued" || retry.ID != before.ID || retry.IdempotencyKey != before.IdempotencyKey || retry.PayloadJSON != before.PayloadJSON || retry.PayloadJSON == "{}" {
		t.Fatalf("retry did not preserve durable encrypted job: before=%#v retry=%#v", before, retry)
	}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("reconcile: processed=%v err=%v", processed, err)
	}
	finished, err := state.Job(ctx, jobID)
	if err != nil || finished.Status != "succeeded" || finished.PayloadJSON != "{}" {
		t.Fatalf("finished job=%#v err=%v", finished, err)
	}
	if len(provisioner.searchReplaceCalls) != 2 || provisioner.searchReplaceCalls[0] != before.IdempotencyKey || provisioner.searchReplaceCalls[1] != before.IdempotencyKey {
		t.Fatalf("search and replace keys=%q want repeated %q", provisioner.searchReplaceCalls, before.IdempotencyKey)
	}
	snapshots, err := state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || len(snapshots) != 1 || snapshots[0].ResticSnapshotID != "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" {
		t.Fatalf("reconciled snapshots=%#v err=%v", snapshots, err)
	}

	// A confirmed host failure is terminal while still retaining its recovery
	// snapshot for the operator.
	token, err = state.CreateWordPressSearchReplacePreview(ctx, owner, "example-com", target.ID, change, preview)
	if err != nil {
		t.Fatal(err)
	}
	failedJobID, err := state.EnqueueWordPressSearchReplace(ctx, owner, "example-com", target.ID, change, token)
	if err != nil {
		t.Fatal(err)
	}
	provisioner.searchReplaceResults = []broker.WordPressSearchReplaceResult{{RecoverySnapshotID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}}
	provisioner.searchReplaceErrors = []error{errors.New("database replacement failed")}
	if processed, err := w.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("known failure: processed=%v err=%v", processed, err)
	}
	failed, err := state.Job(ctx, failedJobID)
	if err != nil || failed.Status != "failed" || failed.PayloadJSON != "{}" || failed.Error != "database replacement failed" {
		t.Fatalf("failed job=%#v err=%v", failed, err)
	}
	snapshots, err = state.ListSiteSnapshots(ctx, "example-com")
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("snapshots after known failure=%#v err=%v", snapshots, err)
	}
}

func TestWorkerVerifiesProviderAndAppliesOwnedDNSRecord(t *testing.T) {
	state, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ConfigureSecretKey(bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner, err := state.CreateOwner(ctx, "operator", "a-secure-test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.CreateSite(ctx, owner, model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: state, Provisioner: &fakeProvisioner{}, DNS: fakeDNS{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	provider, err := state.CreateDNSProvider(ctx, owner, model.DNSProvider{Name: "Cloudflare", Kind: model.DNSCloudflare, ZoneID: "zone123", APIToken: "a-cloudflare-token-long-enough"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := state.CreateDNSRecord(ctx, owner, model.DNSRecord{SiteID: "example-com", ProviderID: provider.ID, Name: "example.com", Type: "A", Value: "192.0.2.10", TTL: 300}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := state.ListSiteDNSRecords(ctx, "example-com")
	if err != nil || len(records) != 1 || records[0].RemoteID != "remote-record" || records[0].Status != "active" {
		t.Fatalf("records=%#v err=%v", records, err)
	}
}
