package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type stagingRunner struct {
	calls          [][]string
	failContaining string
}

type stagingOutput struct{}

func (stagingOutput) Output(context.Context, string, ...string) ([]byte, error) {
	return []byte(`["wp_options","wp_posts","wp_postmeta"]`), nil
}

func (r *stagingRunner) Run(ctx context.Context, executable string, args ...string) error {
	r.calls = append(r.calls, append([]string{executable}, args...))
	if r.failContaining != "" && strings.Contains(strings.Join(args, " "), r.failContaining) {
		return errors.New("injected staging runner failure")
	}
	if executable == "/usr/bin/cp" {
		return exec.CommandContext(ctx, executable, args...).Run()
	}
	if executable != "/usr/sbin/runuser" {
		return nil
	}
	path := ""
	for _, argument := range args {
		if strings.HasPrefix(argument, "--path=") {
			path = strings.TrimPrefix(argument, "--path=")
		}
	}
	if path == "" {
		return errors.New("WP-CLI path missing")
	}
	if slices.Contains(args, "download") {
		return os.WriteFile(filepath.Join(path, "wp-load.php"), []byte("<?php\n"), 0640)
	}
	if slices.Contains(args, "create") && slices.Contains(args, "config") {
		return os.WriteFile(filepath.Join(path, "wp-config.php"), []byte("config:"+filepath.Base(filepath.Dir(path))), 0640)
	}
	if index := slices.Index(args, "export"); index >= 0 && index+1 < len(args) {
		return os.WriteFile(args[index+1], []byte("database"), 0600)
	}
	if index := slices.Index(args, "import"); index >= 0 && index+1 < len(args) {
		_, err := os.ReadFile(args[index+1])
		return err
	}
	return nil
}

func TestStagingCloneAndFullDeployPreserveTargetConfiguration(t *testing.T) {
	runner := &stagingRunner{}
	host := testHost(t, runner)
	wpcli := filepath.Join(host.DataRoot, "wp-cli.phar")
	if err := os.MkdirAll(filepath.Dir(wpcli), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wpcli, []byte("test"), 0750); err != nil {
		t.Fatal(err)
	}
	host.Database = fakeDatabase{}
	host.WordPress = &WPCLI{Runner: runner, Path: wpcli}
	host.Output = stagingOutput{}
	restic := filepath.Join(host.DataRoot, "restic")
	if err := os.WriteFile(restic, []byte("test"), 0700); err != nil {
		t.Fatal(err)
	}
	host.ResticPath = restic
	host.Environment = &backupEnvironmentRunner{}

	production := model.Site{ID: "example-com", Domain: "example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active", Environment: "production"}
	productionPublic := filepath.Join(host.SiteRoot, production.ID, "public")
	if err := os.MkdirAll(filepath.Join(productionPublic, "wp-content"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(host.SiteRoot, production.ID, "tmp"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(productionPublic, "wp-config.php"), []byte("production-config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(productionPublic, "index.php"), []byte("production"), 0640); err != nil {
		t.Fatal(err)
	}
	staging := model.Site{ID: "staging-example", Domain: "staging.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "queued", Environment: "staging", ParentSiteID: production.ID}
	if err := host.CreateStaging(context.Background(), production, staging, "wpx", "a-long-random-staging-password"); err != nil {
		t.Fatal(err)
	}
	stagingPublic := filepath.Join(host.SiteRoot, staging.ID, "public")
	config, err := os.ReadFile(filepath.Join(stagingPublic, "wp-config.php"))
	if err != nil || string(config) != "config:staging-example" {
		t.Fatalf("staging config=%q err=%v", config, err)
	}
	if _, err := os.Stat(filepath.Join(stagingPublic, "wp-content", "mu-plugins", "wpx-staging.php")); err != nil {
		t.Fatalf("staging guard missing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingPublic, "feature.php"), []byte("feature"), 0640); err != nil {
		t.Fatal(err)
	}
	staging.Status = "active"
	if err := os.WriteFile(filepath.Join(productionPublic, "production-only.php"), []byte("remove me"), 0640); err != nil {
		t.Fatal(err)
	}
	inspection, err := host.InspectStaging(context.Background(), staging, production)
	if err != nil {
		t.Fatal(err)
	}
	status := make(map[string]string)
	for _, change := range inspection.Files {
		status[change.Path] = change.Status
	}
	if status["feature.php"] != "added" || status["production-only.php"] != "deleted" || len(inspection.Tables) != 3 {
		t.Fatalf("unexpected staging inspection: %#v", inspection)
	}
	if _, exposed := status["wp-config.php"]; exposed {
		t.Fatal("staging credentials appeared in deployable changes")
	}
	custom := model.StagingSelection{Files: []string{"feature.php", "production-only.php"}, Tables: []string{"wp_options"}}
	customRecovery, err := host.DeployStaging(context.Background(), staging, production, testBackupTarget(), custom, "custom-deploy-job")
	if err != nil {
		t.Fatal(err)
	}
	if customRecovery == "" {
		t.Fatal("custom deployment did not create a recovery snapshot")
	}
	if content, err := os.ReadFile(filepath.Join(productionPublic, "index.php")); err != nil || string(content) != "production" {
		t.Fatalf("unselected production file changed: %q err=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(productionPublic, "feature.php")); err != nil || string(content) != "feature" {
		t.Fatalf("selected file was not deployed: %q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(productionPublic, "production-only.php")); !os.IsNotExist(err) {
		t.Fatalf("selected deletion was not deployed: %v", err)
	}
	foundTableExport := false
	for _, call := range runner.calls {
		if slices.Contains(call, "--tables=wp_options") {
			foundTableExport = true
		}
	}
	if !foundTableExport {
		t.Fatalf("selected database table was not exported: %#v", runner.calls)
	}
	// The durable worker may replay after the host committed the changes but its
	// response was lost. Converged files must remain valid inputs on that retry.
	if _, err := host.DeployStaging(context.Background(), staging, production, testBackupTarget(), custom, "custom-deploy-job"); err != nil {
		t.Fatalf("retrying converged custom deployment: %v", err)
	}
	recoveryID, err := host.DeployStaging(context.Background(), staging, production, testBackupTarget(), model.StagingSelection{Full: true}, "deploy-job")
	if err != nil {
		t.Fatal(err)
	}
	if recoveryID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected recovery snapshot %q", recoveryID)
	}
	config, err = os.ReadFile(filepath.Join(productionPublic, "wp-config.php"))
	if err != nil || string(config) != "production-config" {
		t.Fatalf("production config=%q err=%v", config, err)
	}
	if _, err := os.Stat(filepath.Join(productionPublic, "feature.php")); err != nil {
		t.Fatalf("deployed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(productionPublic, "wp-content", "mu-plugins", "wpx-staging.php")); !os.IsNotExist(err) {
		t.Fatalf("staging guard leaked into production: %v", err)
	}
}
