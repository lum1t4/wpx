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

type cronRunner struct {
	calls                   [][]string
	failInstall, failRemove bool
	failCronActive          bool
}

func (r *cronRunner) Run(_ context.Context, executable string, args ...string) error {
	call := append([]string{executable}, args...)
	r.calls = append(r.calls, call)
	if r.failCronActive && executable == "/usr/bin/systemctl" {
		return errors.New("cron inactive")
	}
	if r.failInstall && executable == "/usr/bin/install" {
		return errors.New("install failed")
	}
	if r.failRemove && executable == "/usr/bin/rm" {
		return errors.New("remove failed")
	}
	return nil
}

type cronIdentity struct{}

func (cronIdentity) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{Name: "wpxsite", UID: 1001, GID: 1001}, nil
}

func cronTestHost(t *testing.T, runner *cronRunner) *Host {
	t.Helper()
	root := t.TempDir()
	h := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	h.CronRoot = filepath.Join(root, "cron.d")
	if err := os.MkdirAll(h.CronRoot, 0755); err != nil {
		t.Fatal(err)
	}
	h.Runner, h.Identities = runner, cronIdentity{}
	return h
}

func TestApplyCronScheduleQuotesEveryArgumentAndRunsAsSiteUser(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "active"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "*/5 * * * *", Command: []string{"php", "it's safe", "$(touch /root/no)", "100%"}, Enabled: true, ApplyStatus: "pending"}
	if err := h.ApplyCronSchedule(context.Background(), site, schedule, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(h.CronRoot, cronFileName(site.ID, schedule.ID))
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "*/5 * * * * wpxsite 'php' 'it'\\''s safe' '$(touch /root/no)' '100\\%'") {
		t.Fatalf("unsafe or malformed crontab line:\n%s", text)
	}
}

func TestDisabledSiteKeepsDesiredCronWithoutInstallingIt(t *testing.T) {
	runner := &cronRunner{failCronActive: true}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "disabled"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
	if err := h.ApplyCronSchedule(context.Background(), site, schedule, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.CronRoot, cronFileName(site.ID, schedule.ID))); !os.IsNotExist(err) {
		t.Fatalf("disabled site cron exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.DataRoot, "cron", cronFileName(site.ID, schedule.ID))); err != nil {
		t.Fatalf("disabled site desired cron missing: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("disabled schedule checked cron service: %#v", runner.calls)
	}
}

func TestEnabledCronRequiresActiveCronService(t *testing.T) {
	runner := &cronRunner{failCronActive: true}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "active"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
	err := h.ApplyCronSchedule(context.Background(), site, schedule, false)
	if err == nil || !strings.Contains(err.Error(), "install and start cron") {
		t.Fatalf("inactive cron service error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.CronRoot, cronFileName(site.ID, schedule.ID))); !os.IsNotExist(err) {
		t.Fatalf("schedule activated while cron was inactive: %v", err)
	}
}

func TestCronRemovalDoesNotRequireActiveService(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "active"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
	if err := h.ApplyCronSchedule(context.Background(), site, schedule, false); err != nil {
		t.Fatal(err)
	}
	runner.calls, runner.failCronActive = nil, true
	if err := h.ApplyCronSchedule(context.Background(), site, schedule, true); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("removal checked cron service: %#v", runner.calls)
	}
}

func TestWordPressCronDoesNotDisableInternalRunnerWhenCronServiceInactive(t *testing.T) {
	runner := &cronRunner{failCronActive: true}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "wp-cron-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(h.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(public, "wp-config.php")
	original := "<?php\n/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/5 * * * *", ApplyStatus: "pending"}
	err := h.ApplyWordPressCronReplacement(context.Background(), site, setting)
	if err == nil || !strings.Contains(err.Error(), "install and start cron") {
		t.Fatalf("inactive WordPress cron error = %v", err)
	}
	content, _ := os.ReadFile(configPath)
	if string(content) != original {
		t.Fatal("internal WordPress cron was disabled while cron.service was inactive")
	}
}

func TestWordPressCronFailureOrderingIsReversible(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "wp-cron-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(h.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(public, 0755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(public, "wp-config.php")
	original := "<?php\n/* That's all, stop editing! Happy publishing. */\n"
	if err := os.WriteFile(configPath, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/5 * * * *", ApplyStatus: "pending"}
	target := filepath.Join(h.CronRoot, cronFileName(site.ID, "wordpress"))
	if err := os.WriteFile(target, []byte("unmanaged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err == nil {
		t.Fatal("failed cron install succeeded")
	}
	content, _ := os.ReadFile(configPath)
	if string(content) != original {
		t.Fatal("internal WordPress cron was disabled before external cron installed")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(configPath)
	if !strings.Contains(string(content), wpCronMarker) {
		t.Fatal("managed DISABLE_WP_CRON marker missing")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	setting.Replaced = false
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err == nil {
		t.Fatal("failed cron removal succeeded")
	}
	content, _ = os.ReadFile(configPath)
	if strings.Contains(string(content), "DISABLE_WP_CRON") {
		t.Fatal("visitor-triggered cron was not restored before external removal")
	}
}

func TestWordPressCronRefusesUnmanagedDisableConstant(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "wp-cron-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(h.SiteRoot, site.ID, "public")
	_ = os.MkdirAll(public, 0755)
	_ = os.WriteFile(filepath.Join(public, "wp-config.php"), []byte("<?php\ndefine('DISABLE_WP_CRON', true);\n/* That's all, stop editing! */\n"), 0640)
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/5 * * * *", ApplyStatus: "pending"}
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err == nil || !strings.Contains(err.Error(), "outside WPX management") {
		t.Fatalf("unmanaged constant accepted: %v", err)
	}
}

func TestCronRefusesUnmanagedAndSymlinkTargets(t *testing.T) {
	for _, targetKind := range []string{"unmanaged", "symlink"} {
		t.Run(targetKind, func(t *testing.T) {
			runner := &cronRunner{}
			h := cronTestHost(t, runner)
			site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "active"}
			schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
			target := filepath.Join(h.CronRoot, cronFileName(site.ID, schedule.ID))
			if targetKind == "unmanaged" {
				_ = os.WriteFile(target, []byte("root command\n"), 0644)
			} else {
				outside := filepath.Join(t.TempDir(), "outside")
				_ = os.WriteFile(outside, []byte("keep"), 0644)
				_ = os.Symlink(outside, target)
			}
			if err := h.ApplyCronSchedule(context.Background(), site, schedule, false); err == nil {
				t.Fatal("unsafe cron target accepted")
			}
			if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "/usr/bin/systemctl is-active --quiet cron.service" {
				t.Fatalf("unexpected calls for unsafe target: %#v", runner.calls)
			}
		})
	}
}

func TestWordPressCronRefusesSymlinkConfig(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "wp-cron-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(h.SiteRoot, site.ID, "public")
	_ = os.MkdirAll(public, 0755)
	outside := filepath.Join(t.TempDir(), "outside.php")
	original := []byte("<?php\n/* That's all, stop editing! */\n")
	_ = os.WriteFile(outside, original, 0640)
	_ = os.Symlink(outside, filepath.Join(public, "wp-config.php"))
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/5 * * * *", ApplyStatus: "pending"}
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err == nil {
		t.Fatal("symlink wp-config accepted")
	}
	content, _ := os.ReadFile(outside)
	if string(content) != string(original) {
		t.Fatal("symlink target was modified")
	}
}

func TestWordPressCronRejectsSymlinkedPublicDirectory(t *testing.T) {
	runner := &cronRunner{}
	h := cronTestHost(t, runner)
	site := model.Site{ID: "wp-cron-site", Domain: "wp.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	outside := t.TempDir()
	original := []byte("<?php\n/* That's all, stop editing! */\n")
	if err := os.WriteFile(filepath.Join(outside, "wp-config.php"), original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(siteDir, "public")); err != nil {
		t.Fatal(err)
	}
	setting := model.WordPressCronSetting{SiteID: site.ID, Replaced: true, Expression: "*/5 * * * *", ApplyStatus: "pending"}
	if err := h.ApplyWordPressCronReplacement(context.Background(), site, setting); err == nil {
		t.Fatal("symlinked public directory was accepted")
	}
	content, err := os.ReadFile(filepath.Join(outside, "wp-config.php"))
	if err != nil || string(content) != string(original) {
		t.Fatalf("outside wp-config.php changed: %q, %v", content, err)
	}
}

func TestCronRejectsSymlinkedRootAncestor(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "redirect")); err != nil {
		t.Fatal(err)
	}
	h := cronTestHost(t, &cronRunner{})
	h.CronRoot = filepath.Join(base, "redirect")
	site := model.Site{ID: "cron-site", Domain: "cron.example.com", Kind: model.Static, Status: "active"}
	schedule := model.CronSchedule{ID: "cron_12345678", SiteID: site.ID, Name: "task", Expression: "0 * * * *", Command: []string{"php", "task"}, Enabled: true, ApplyStatus: "pending"}
	if err := h.ApplyCronSchedule(context.Background(), site, schedule, false); err == nil {
		t.Fatal("symlinked cron root was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cron root escape created outside files: %v, %v", entries, err)
	}
}
