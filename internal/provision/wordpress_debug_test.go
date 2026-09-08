package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/model"
)

type wordpressDebugIdentity struct{}

func (wordpressDebugIdentity) Ensure(context.Context, model.Site, string) (Identity, error) {
	return Identity{Name: "wpxsite", UID: os.Getuid(), GID: os.Getgid()}, nil
}

type wordpressDebugRunner struct {
	calls         [][]string
	swapDirectory string
}

func (r *wordpressDebugRunner) Run(_ context.Context, executable string, args ...string) error {
	r.calls = append(r.calls, append([]string{executable}, args...))
	return nil
}

func (r *wordpressDebugRunner) LintPHPFile(_ context.Context, _ Identity, executable string, file *os.File) error {
	r.calls = append(r.calls, []string{executable, "-l", "-f", "/proc/self/fd/3"})
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	content, err := os.ReadFile(file.Name())
	if err != nil {
		content, err = io.ReadAll(file)
	}
	if err != nil {
		return err
	}
	if strings.Contains(string(content), "if (") {
		return errors.New("PHP parse error")
	}
	if r.swapDirectory != "" {
		entries, _ := os.ReadDir(r.swapDirectory)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".wpx-save-") {
				path := filepath.Join(r.swapDirectory, entry.Name())
				_ = os.Remove(path)
				_ = os.WriteFile(path, []byte("<?php syntax error"), 0640)
				break
			}
		}
	}
	return nil
}

func wordpressDebugTestHost(t *testing.T) (*Host, model.Site, string) {
	t.Helper()
	root := t.TempDir()
	host := DefaultHost(filepath.Join(root, "sites"), filepath.Join(root, "data"))
	host.Runner = &wordpressDebugRunner{}
	host.Identities = wordpressDebugIdentity{}
	site := model.Site{ID: "debug-site", Domain: "debug.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	public := filepath.Join(host.SiteRoot, site.ID, "public")
	if err := os.MkdirAll(filepath.Join(host.SiteRoot, site.ID, "tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(public, "wp-config.php")
	if err := os.WriteFile(config, []byte("<?php\n\n/* That's all, stop editing! Happy publishing. */\n"), 0640); err != nil {
		t.Fatal(err)
	}
	return host, site, config
}

func TestWordPressDebugLintsTemporaryConfigBeforeRename(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original := []byte("<?php\nif (\n/* That's all, stop editing! */\n")
	if err := os.WriteFile(config, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil || !strings.Contains(err.Error(), "syntax") {
		t.Fatalf("invalid configuration lint error = %v", err)
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(original) {
		t.Fatal("lint failure replaced wp-config.php")
	}
}

func TestWordPressDebugRejectsTemporaryEntrySwapAfterLint(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original, _ := os.ReadFile(config)
	host.Runner.(*wordpressDebugRunner).swapDirectory = filepath.Dir(config)
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil || !strings.Contains(err.Error(), "changed during syntax validation") {
		t.Fatalf("temporary swap error = %v", err)
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(original) {
		t.Fatal("temporary entry swap replaced wp-config.php")
	}
}

func TestWordPressDebugToggleUsesPrivateLogAndHidesDisplay(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	if err := host.SetWordPressDebug(context.Background(), site, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(config)
	configInfo, err := os.Stat(config)
	if err != nil {
		t.Fatal(err)
	}
	if configInfo.Mode().Perm() != 0640 {
		t.Fatalf("wp-config.php mode changed: %v", configInfo.Mode().Perm())
	}
	logPath := host.wordpressDebugLogPath(site)
	for _, wanted := range []string{"define('WP_DEBUG', true);", "define('WP_DEBUG_DISPLAY', false);", "define('WP_DEBUG_LOG', '" + logPath + "');"} {
		if !strings.Contains(string(content), wanted) {
			t.Fatalf("managed config missing %q:\n%s", wanted, content)
		}
	}
	if strings.HasPrefix(logPath, filepath.Join(host.SiteRoot, site.ID, "public")) {
		t.Fatalf("debug log is web-readable: %s", logPath)
	}
	info, err := os.Stat(logPath)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("private log mode = %v, error = %v", info.Mode().Perm(), err)
	}
	status, err := host.WordPressDebugStatus(context.Background(), site)
	if err != nil || !status.Enabled || !status.Known || !status.Managed || !status.LogExists {
		t.Fatalf("status = %#v, error = %v", status, err)
	}
	beforeSecondEnable, _ := os.ReadFile(config)
	if err := host.SetWordPressDebug(context.Background(), site, true); err != nil {
		t.Fatal(err)
	}
	afterSecondEnable, _ := os.ReadFile(config)
	if string(afterSecondEnable) != string(beforeSecondEnable) {
		t.Fatal("enabling an already enabled configuration was not idempotent")
	}
	if err := host.SetWordPressDebug(context.Background(), site, false); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(config)
	for _, wanted := range []string{"define('WP_DEBUG', false);", "define('WP_DEBUG_LOG', false);", "define('WP_DEBUG_DISPLAY', false);"} {
		if !strings.Contains(string(content), wanted) {
			t.Fatalf("disabled block missing %q:\n%s", wanted, content)
		}
	}
}

func TestWordPressDebugRefusesUnmanagedOrPartialDefinitionsWithoutChangingConfig(t *testing.T) {
	for name, addition := range map[string]string{
		"complex expression":   "define ( \"WP_DEBUG\" , getenv('DEBUG') ); // keep this\n",
		"multiline definition": "define(\n  'WP_DEBUG',\n  false\n);\n",
		"uppercase function":   "DEFINE('WP_DEBUG', true);\n",
		"heredoc body":         "$text = <<<'PHP'\ndefine('WP_DEBUG', true);\nPHP;\n",
		"partial owned block":  wpDebugStart + "\ndefine('WP_DEBUG', true);\n",
	} {
		t.Run(name, func(t *testing.T) {
			host, site, config := wordpressDebugTestHost(t)
			original := []byte("<?php\n" + addition + "/* That's all, stop editing! Happy publishing. */\n")
			if err := os.WriteFile(config, original, 0640); err != nil {
				t.Fatal(err)
			}
			if err := host.SetWordPressDebug(context.Background(), site, true); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
			after, _ := os.ReadFile(config)
			if string(after) != string(original) {
				t.Fatal("failed update changed wp-config.php")
			}
			if _, err := os.Stat(host.wordpressDebugLogPath(site)); !os.IsNotExist(err) {
				t.Fatalf("failed update did not roll back a newly created log: %v", err)
			}
		})
	}
}

func TestWordPressDebugAdoptsStandardLiteralDefinitionsAndPreservesComments(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original := `<?php
define( 'WP_DEBUG', false ); // standard default
define("WP_DEBUG_LOG", '/tmp/name;with-semicolon.log'); # operator note
define('WP_DEBUG_DISPLAY', false);
/* That's all, stop editing! Happy publishing. */
`
	if err := os.WriteFile(config, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	before, err := host.WordPressDebugStatus(context.Background(), site)
	if err != nil || before.Enabled || !before.Known || before.Managed {
		t.Fatalf("standard WordPress status = %#v, error = %v", before, err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(config)
	for _, comment := range []string{"// standard default", "# operator note"} {
		if !strings.Contains(string(content), comment) {
			t.Fatalf("adoption lost comment %q:\n%s", comment, content)
		}
	}
	if strings.Count(string(content), "define('WP_DEBUG',") != 1 {
		t.Fatalf("adoption left duplicate constants:\n%s", content)
	}
}

func TestWordPressDebugRejectsDuplicateLiteralDefinitions(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original := []byte("<?php\ndefine('WP_DEBUG', false);\ndefine(\"WP_DEBUG\", true);\n/* That's all, stop editing! */\n")
	if err := os.WriteFile(config, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil {
		t.Fatal("accepted duplicate literal definitions")
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(original) {
		t.Fatal("duplicate rejection changed wp-config.php")
	}
}

func TestWordPressDebugLogReadIsBoundedAndClearRejectsSymlink(t *testing.T) {
	host, site, _ := wordpressDebugTestHost(t)
	if err := host.SetWordPressDebug(context.Background(), site, true); err != nil {
		t.Fatal(err)
	}
	logPath := host.wordpressDebugLogPath(site)
	content := strings.Repeat("debug entry\n", model.WordPressDebugLogLimit/6)
	if err := os.WriteFile(logPath, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
	log, err := host.ReadWordPressDebugLog(context.Background(), site)
	if err != nil || !log.Truncated || len(log.Content) > model.WordPressDebugLogLimit {
		t.Fatalf("bounded log = size %d truncated %t, error = %v", len(log.Content), log.Truncated, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, logPath); err != nil {
		t.Fatal(err)
	}
	if err := host.ClearWordPressDebugLog(context.Background(), site); err == nil {
		t.Fatal("clear followed a symbolic link")
	}
	after, _ := os.ReadFile(outside)
	if string(after) != "keep" {
		t.Fatal("clear modified the symlink target")
	}
}

func TestWordPressDebugConfigRejectsSymlinkedPublicRoot(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	outside := t.TempDir()
	if err := os.Rename(filepath.Dir(config), filepath.Dir(config)+"-real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Dir(config)); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil {
		t.Fatal("accepted a symlinked public root")
	}
}

func TestWordPressDebugLogSetupFailureLeavesConfigurationUntouched(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original, _ := os.ReadFile(config)
	tmp := filepath.Join(host.SiteRoot, site.ID, "tmp")
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), tmp); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil {
		t.Fatal("accepted a symlinked private log directory")
	}
	after, _ := os.ReadFile(config)
	if string(after) != string(original) {
		t.Fatal("log setup failure changed wp-config.php")
	}
}

func TestWordPressDebugRejectsOversizedConfig(t *testing.T) {
	host, site, config := wordpressDebugTestHost(t)
	original := append([]byte("<?php\n"), make([]byte, maxEditableFile+1)...)
	if err := os.WriteFile(config, original, 0640); err != nil {
		t.Fatal(err)
	}
	if err := host.SetWordPressDebug(context.Background(), site, true); err == nil {
		t.Fatal("accepted wp-config.php over the managed size limit")
	}
	after, _ := os.ReadFile(config)
	if len(after) != len(original) {
		t.Fatal("oversized wp-config.php changed")
	}
}

func TestUpdateWordPressDebugIgnoresConstantNamesInCommentsAndStrings(t *testing.T) {
	content := []byte("<?php\n// WP_DEBUG and define('WP_DEBUG_LOG', true)\n$x = \"WP_DEBUG_DISPLAY\";\n$command = `echo start\ndefine('WP_DEBUG', true);\necho end`;\n/* That's all, stop editing! */\n")
	updated, changed, err := updateWordPressDebug(content, true, "/private/debug.log")
	if err != nil || !changed || !strings.Contains(string(updated), "$command = `echo start\ndefine('WP_DEBUG', true);\necho end`;") {
		t.Fatalf("update = %q, changed=%t error=%v", updated, changed, err)
	}
}

func TestInspectWordPressDebugDoesNotHideUnsafeUnmanagedDebug(t *testing.T) {
	content := []byte("<?php\ndefine('WP_DEBUG', true);\ndefine('WP_DEBUG_LOG', true);\ndefine('WP_DEBUG_DISPLAY', true);\n")
	enabled, known, managed := inspectWordPressDebug(content, "/private/debug.log")
	if !enabled || !known || managed {
		t.Fatalf("unmanaged debug reported enabled=%t known=%t managed=%t", enabled, known, managed)
	}
}
