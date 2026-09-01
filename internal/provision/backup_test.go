package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type backupEnvironmentRunner struct {
	existingSnapshot string
	restoredPublic   string
	restoredDatabase bool
	calls            [][]string
}

func (r *backupEnvironmentRunner) RunEnv(_ context.Context, _ []string, _ string, args ...string) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	if slices.Contains(args, "restore") && r.restoredPublic != "" {
		targetIndex := slices.Index(args, "--target")
		if targetIndex == -1 || targetIndex+1 >= len(args) {
			return errors.New("missing restore target")
		}
		restored := filepath.Join(args[targetIndex+1], strings.TrimPrefix(r.restoredPublic, string(filepath.Separator)))
		if err := os.MkdirAll(restored, 0750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(restored, "index.html"), []byte("restored"), 0640); err != nil {
			return err
		}
		if r.restoredDatabase {
			return os.WriteFile(filepath.Join(args[targetIndex+1], "database.sql"), []byte("database"), 0600)
		}
		return nil
	}
	return nil
}

func (r *backupEnvironmentRunner) OutputEnv(_ context.Context, _ []string, executable string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{executable}, args...))
	if slices.Contains(args, "snapshots") {
		jobProbe := false
		for _, argument := range args {
			if strings.HasPrefix(argument, "job:") {
				jobProbe = true
			}
		}
		if jobProbe {
			if r.existingSnapshot != "" {
				return []byte(`[{"id":"` + r.existingSnapshot + `"}]`), nil
			}
			return nil, errors.New("no matching snapshot")
		}
		return []byte(`[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`), nil
	}
	return []byte("{\"message_type\":\"status\"}\n{\"message_type\":\"summary\",\"snapshot_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n"), nil
}

func TestBackupSiteCreatesTaggedSnapshot(t *testing.T) {
	root := t.TempDir()
	restic := filepath.Join(root, "restic")
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	siteRoot := filepath.Join(root, "sites")
	if err := os.MkdirAll(filepath.Join(siteRoot, "example-com", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	environment := &backupEnvironmentRunner{}
	host := &Host{SiteRoot: siteRoot, DataRoot: filepath.Join(root, "data"), ResticPath: restic, Environment: environment, Identities: currentIdentity{}}
	result, err := host.BackupSite(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}, testBackupTarget(), model.BackupRetention{}, "job-123")
	if err != nil {
		t.Fatal(err)
	}
	if result.SnapshotID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(environment.calls) != 3 {
		t.Fatalf("snapshot=%q calls=%#v", result.SnapshotID, environment.calls)
	}
	backupCall := environment.calls[1]
	if !slices.Contains(backupCall, "job:job-123") || !slices.Contains(backupCall, "wpx-login-*.php") {
		t.Fatalf("backup command lacks retry tag or login exclusion: %#v", backupCall)
	}
}

func TestBackupSiteReturnsExistingSnapshotOnRetry(t *testing.T) {
	root := t.TempDir()
	restic := filepath.Join(root, "restic")
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	environment := &backupEnvironmentRunner{existingSnapshot: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	host := &Host{SiteRoot: filepath.Join(root, "sites"), DataRoot: filepath.Join(root, "data"), ResticPath: restic, Environment: environment}
	result, err := host.BackupSite(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}, testBackupTarget(), model.BackupRetention{}, "job-123")
	if err != nil || result.SnapshotID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || len(environment.calls) != 2 {
		t.Fatalf("snapshot=%q calls=%d err=%v", result.SnapshotID, len(environment.calls), err)
	}
}

func TestScheduledBackupAppliesSiteScopedRetention(t *testing.T) {
	root := t.TempDir()
	restic := filepath.Join(root, "restic")
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(root, "sites", "example-com", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	environment := &backupEnvironmentRunner{}
	host := &Host{SiteRoot: filepath.Join(root, "sites"), DataRoot: filepath.Join(root, "data"), ResticPath: restic, Environment: environment, Identities: currentIdentity{}}
	retention := model.BackupRetention{KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 6}
	result, err := host.BackupSite(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}, testBackupTarget(), retention, "scheduled-job")
	if err != nil {
		t.Fatal(err)
	}
	if !result.RetentionApplied || len(result.RetainedSnapshotIDs) != 1 {
		t.Fatalf("retention result was not reported: %#v", result)
	}
	var forget []string
	for _, call := range environment.calls {
		if slices.Contains(call, "forget") {
			forget = call
			break
		}
	}
	if forget == nil {
		t.Fatalf("retention command missing: %#v", environment.calls)
	}
	for _, expected := range []string{"forget", "site:example-com", "--group-by", "tags", "--keep-daily", "7", "--keep-weekly", "4", "--keep-monthly", "6", "--prune"} {
		if !slices.Contains(forget, expected) {
			t.Fatalf("retention command missing %q: %#v", expected, forget)
		}
	}
}

func TestRestoreStaticSiteSwitchesFilesAndCreatesRollbackSnapshot(t *testing.T) {
	root := t.TempDir()
	restic := filepath.Join(root, "restic")
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	siteRoot := filepath.Join(root, "sites")
	public := filepath.Join(siteRoot, "example-com", "public")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("current"), 0640); err != nil {
		t.Fatal(err)
	}
	environment := &backupEnvironmentRunner{restoredPublic: public}
	host := &Host{SiteRoot: siteRoot, DataRoot: filepath.Join(root, "data"), ResticPath: restic, Environment: environment, Identities: currentIdentity{}}
	rollbackID, err := host.RestoreSite(context.Background(), model.Site{ID: "example-com", Domain: "example.com", Kind: model.Static}, testBackupTarget(), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "restore-job")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(public, "index.html"))
	if err != nil || string(content) != "restored" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	if rollbackID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected rollback snapshot %q", rollbackID)
	}
}

func testBackupTarget() model.BackupTarget {
	return model.BackupTarget{
		ID: "bkt_test", Name: "Object storage", Kind: model.BackupS3, Status: "active",
		Endpoint: "https://objects.example.com", Bucket: "backups", Region: "us-east-1", BucketLookup: "path",
		AccessKey: "access", SecretKey: "secret", RepositoryPassword: "a-repository-password-long-enough",
	}
}

func TestGoogleDriveBackupUsesPinnedRcloneProgramAndEnvironmentConfig(t *testing.T) {
	root := t.TempDir()
	rclone := filepath.Join(root, "rclone")
	if err := os.WriteFile(rclone, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	host := &Host{DataRoot: filepath.Join(root, "data"), RclonePath: rclone}
	target := model.BackupTarget{
		Name: "Drive", Kind: model.BackupGoogleDrive, DriveFolder: "WPX Backups",
		GoogleClientID: "123-example.apps.googleusercontent.com", GoogleClientSecret: "client-secret-value",
		GoogleToken:        `{"access_token":"short-lived","refresh_token":"refresh-token"}`,
		RepositoryPassword: "a-repository-password-long-enough",
	}
	environment, options, err := host.backupEnvironment(target)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(environment, "RCLONE_CONFIG_WPXDRIVE_SCOPE=drive.file") || !slices.Contains(environment, "RCLONE_CONFIG_WPXDRIVE_TOKEN="+target.GoogleToken) || !slices.Contains(options, "rclone.program="+rclone) {
		t.Fatalf("environment=%#v options=%#v", environment, options)
	}
}
