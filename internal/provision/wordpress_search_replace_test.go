package provision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

type searchReplaceOutputRunner struct {
	outputs [][]byte
	calls   [][]string
	errAt   int
}

func (runner *searchReplaceOutputRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("unbounded output must not be used")
}

func (runner *searchReplaceOutputRunner) OutputBounded(_ context.Context, _ int, executable string, args ...string) ([]byte, error) {
	runner.calls = append(runner.calls, append([]string{executable}, args...))
	if runner.errAt > 0 && len(runner.calls) == runner.errAt {
		return nil, errors.New("injected command failure")
	}
	if len(runner.outputs) == 0 {
		return nil, errors.New("missing output fixture")
	}
	result := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	return result, nil
}

func searchReplaceHost(t *testing.T, output OutputRunner) (*Host, model.Site) {
	t.Helper()
	host := testHost(t, &recordRunner{})
	host.Output = output
	host.WordPress = &WPCLI{Path: "/usr/local/lib/wpx/wp-cli.phar"}
	if err := os.MkdirAll(host.DataRoot, 0700); err != nil {
		t.Fatal(err)
	}
	site := model.Site{ID: "replace-site", Domain: "replace.example.com", Kind: model.WordPress, PHPVersion: "8.4", Status: "active"}
	createWordPressPublicFixture(t, host, site)
	return host, site
}

func TestWordPressSearchReplaceUsesLivePrefixCSVAndLiteralCount(t *testing.T) {
	runner := &searchReplaceOutputRunner{outputs: [][]byte{[]byte("wp_options,wp_posts\n"), []byte("7\n")}}
	host, site := searchReplaceHost(t, runner)
	change := model.WordPressSearchReplace{Search: `a:1:{s:3:"url";s:19:"https://old.test";}`, Replace: `a:1:{s:3:"url";s:19:"https://new.test";}`}
	result, err := host.SearchReplaceWordPress(context.Background(), site, model.BackupTarget{}, change, true, "preview-key")
	if err != nil {
		t.Fatal(err)
	}
	if result.Tables != 2 || result.Replacements != 7 || len(result.TableResults) != 0 {
		t.Fatalf("unexpected preview result: %#v", result)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("WP-CLI call count = %d, want 2", len(runner.calls))
	}
	discovery := runner.calls[0]
	for _, required := range []string{"--skip-plugins", "--skip-themes", "db", "tables", "--all-tables-with-prefix", "--format=csv"} {
		if !slices.Contains(discovery, required) {
			t.Fatalf("table discovery omitted %q: %#v", required, discovery)
		}
	}
	call := runner.calls[1]
	for _, required := range []string{"search-replace", change.Search, change.Replace, "--all-tables-with-prefix", "--skip-columns=guid", "--precise", "--format=count", "--dry-run"} {
		if !slices.Contains(call, required) {
			t.Fatalf("search and replace call omitted %q: %#v", required, call)
		}
	}
	if slices.Contains(call, "--regex") || slices.Contains(call, "--all-tables") {
		t.Fatalf("unsafe search mode reached WP-CLI: %#v", call)
	}
}

func TestWordPressSearchReplaceApplyInvalidatesOnlyManagedSiteCache(t *testing.T) {
	runner := &searchReplaceOutputRunner{outputs: [][]byte{[]byte("wp_options,wp_posts\n"), []byte("2\n"), []byte("Success\n")}}
	host, site := searchReplaceHost(t, runner)
	change := model.WordPressSearchReplace{Search: "old", Replace: "new"}
	result, err := host.runWordPressSearchReplace(context.Background(), site, change, false)
	if err != nil || result.Replacements != 2 {
		t.Fatalf("apply result = %#v, error = %v", result, err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("WP-CLI call count = %d, want table discovery, replace, cache invalidation", len(runner.calls))
	}
	cacheCall := strings.Join(runner.calls[2], " ")
	if !strings.Contains(cacheCall, " eval ") || !strings.Contains(cacheCall, "wpx:"+site.ID+":") || strings.Contains(cacheCall, "FLUSHDB") || strings.Contains(cacheCall, "cache flush") {
		t.Fatalf("cache invalidation was not site-scoped: %s", cacheCall)
	}
}

func TestParseWordPressPrefixTablesAcceptsQuotedHeaderAndRejectsInjection(t *testing.T) {
	tables, err := parseWordPressPrefixTables([]byte("\"Tables_in_database\"\r\n\"wp_options\"\r\n\"wp_plugin_table\"\r\n"))
	if err != nil || !slices.Equal(tables, []string{"wp_options", "wp_plugin_table"}) {
		t.Fatalf("CSV tables = %#v, error = %v", tables, err)
	}
	live, err := parseWordPressPrefixTables([]byte("wp_commentmeta,wp_comments,wp_options,wp_posts\n"))
	if err != nil || !slices.Equal(live, []string{"wp_commentmeta", "wp_comments", "wp_options", "wp_posts"}) {
		t.Fatalf("live WP-CLI CSV tables = %#v, error = %v", live, err)
	}
	for _, invalid := range []string{"", "wp_options\nwp_options\n", "wp_options\n../../users\n", "wp_options\n\"unterminated\n"} {
		if _, err := parseWordPressPrefixTables([]byte(invalid)); err == nil {
			t.Fatalf("invalid CSV table list accepted: %q", invalid)
		}
	}
}

func TestWordPressSearchReplaceMarkerPreventsBlindReplay(t *testing.T) {
	host, _ := searchReplaceHost(t, &searchReplaceOutputRunner{})
	recovery, err := host.openWordPressSearchReplaceRecovery("durable-job")
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.root.Close()
	recoveryID := strings.Repeat("a", 64)
	fingerprint := strings.Repeat("b", 64)
	runs := 0
	run := func() (broker.WordPressSearchReplaceResult, error) {
		runs++
		return broker.WordPressSearchReplaceResult{Tables: 1, Replacements: 3, TableResults: []broker.WordPressSearchReplaceTableResult{{Name: "wp_options", Replacements: 3}}}, nil
	}
	first, err := applyWordPressSearchReplace(recovery, fingerprint, recoveryID, run)
	if err != nil || first.RecoverySnapshotID != recoveryID {
		t.Fatalf("first apply = %#v, %v", first, err)
	}
	second, err := applyWordPressSearchReplace(recovery, fingerprint, recoveryID, run)
	if err != nil || second.Replacements != 3 || runs != 1 {
		t.Fatalf("completed replay reapplied mutation: result=%#v runs=%d error=%v", second, runs, err)
	}
	if _, err := applyWordPressSearchReplace(recovery, strings.Repeat("c", 64), recoveryID, run); err == nil || runs != 1 {
		t.Fatalf("fingerprint mismatch did not fail closed: runs=%d error=%v", runs, err)
	}
}

func TestWordPressSearchReplaceStartedOrFailedMutationRequiresRecovery(t *testing.T) {
	host, _ := searchReplaceHost(t, &searchReplaceOutputRunner{})
	recovery, err := host.openWordPressSearchReplaceRecovery("interrupted-job")
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.root.Close()
	recoveryID := strings.Repeat("d", 64)
	fingerprint := strings.Repeat("e", 64)
	runs := 0
	result, err := applyWordPressSearchReplace(recovery, fingerprint, recoveryID, func() (broker.WordPressSearchReplaceResult, error) {
		runs++
		return broker.WordPressSearchReplaceResult{}, errors.New("partial failure")
	})
	if err == nil || result.RecoverySnapshotID != recoveryID || !strings.Contains(err.Error(), recoveryID) {
		t.Fatalf("partial failure did not surface recovery: result=%#v error=%v", result, err)
	}
	if _, err := applyWordPressSearchReplace(recovery, fingerprint, recoveryID, func() (broker.WordPressSearchReplaceResult, error) {
		runs++
		return broker.WordPressSearchReplaceResult{}, nil
	}); err == nil || runs != 1 || !strings.Contains(err.Error(), recoveryID) {
		t.Fatalf("started marker was blindly replayed: runs=%d error=%v", runs, err)
	}
}

func TestWordPressSearchReplaceCompletedReplayDoesNotContactBackup(t *testing.T) {
	host, site := searchReplaceHost(t, &searchReplaceOutputRunner{})
	target := testBackupTarget()
	change := model.WordPressSearchReplace{Search: "old", Replace: "new"}
	jobKey := "completed-job"
	recoveryID := strings.Repeat("f", 64)
	recovery, err := host.openWordPressSearchReplaceRecovery(jobKey)
	if err != nil {
		t.Fatal(err)
	}
	result := broker.WordPressSearchReplaceResult{Tables: 2, Replacements: 4, RecoverySnapshotID: recoveryID}
	if err := recovery.write(wordpressSearchReplaceJournal{Fingerprint: searchReplaceFingerprint(site.ID, target.ID, change), RecoverySnapshotID: recoveryID, Status: "completed", Result: result}); err != nil {
		recovery.root.Close()
		t.Fatal(err)
	}
	recovery.root.Close()
	// This host has no Restic environment. Success therefore proves the local
	// completion marker is inspected before any backup repository call.
	got, err := host.SearchReplaceWordPress(context.Background(), site, target, change, false, jobKey)
	if err != nil || got.RecoverySnapshotID != recoveryID || got.Replacements != 4 {
		t.Fatalf("completed replay = %#v, %v", got, err)
	}
}

func TestWordPressSearchReplaceRecoveryRejectsSymlinkedStateRoot(t *testing.T) {
	host, _ := searchReplaceHost(t, &searchReplaceOutputRunner{})
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(host.DataRoot, "wordpress-search-replace")); err != nil {
		t.Fatal(err)
	}
	if _, err := host.openWordPressSearchReplaceRecovery("job"); err == nil {
		t.Fatal("symlinked search and replace state root was accepted")
	}
}

func TestWordPressSearchReplaceRecoveryRejectsTrailingJournalData(t *testing.T) {
	host, _ := searchReplaceHost(t, &searchReplaceOutputRunner{})
	recovery, err := host.openWordPressSearchReplaceRecovery("corrupt-job")
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.root.Close()
	journal := wordpressSearchReplaceJournal{Fingerprint: strings.Repeat("a", 64), RecoverySnapshotID: strings.Repeat("b", 64), Status: "started"}
	if err := recovery.write(journal); err != nil {
		t.Fatal(err)
	}
	file, err := recovery.root.OpenFile("state.json", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{}"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recovery.read(); err == nil {
		t.Fatal("journal with trailing JSON was accepted")
	}
}
