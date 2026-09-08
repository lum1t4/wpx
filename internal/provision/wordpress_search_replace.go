package provision

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

const (
	maxWordPressSearchReplaceOutput = 1 << 20
	maxWordPressSearchReplaceRows   = 10000
)

var wordpressTableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)
var wordpressSearchReplaceFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type boundedOutputRunner interface {
	OutputBounded(context.Context, int, string, ...string) ([]byte, error)
}

type boundedCommandOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	exceeded bool
}

func (output *boundedCommandOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	remaining := output.limit - len(output.data)
	if remaining <= 0 || len(data) > remaining {
		if remaining > 0 {
			output.data = append(output.data, data[:remaining]...)
		}
		output.exceeded = true
		return len(data), nil
	}
	output.data = append(output.data, data...)
	return len(data), nil
}

func (ExecOutputRunner) OutputBounded(ctx context.Context, limit int, executable string, args ...string) ([]byte, error) {
	output := &boundedCommandOutput{limit: limit}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("run %s: %w", filepath.Base(executable), err)
	}
	if output.exceeded {
		return nil, errors.New("command output exceeds limit")
	}
	return output.data, nil
}

type wordpressSearchReplaceJournal struct {
	Fingerprint        string                              `json:"fingerprint"`
	RecoverySnapshotID string                              `json:"recovery_snapshot_id"`
	Status             string                              `json:"status"`
	Result             broker.WordPressSearchReplaceResult `json:"result,omitempty"`
}

type wordpressSearchReplaceRecovery struct{ root *os.Root }

// SearchReplaceWordPress runs an apply at most once. An uncertain started
// marker fails closed and retains the remote snapshot for an explicit restore.
func (h *Host) SearchReplaceWordPress(ctx context.Context, site model.Site, target model.BackupTarget, change model.WordPressSearchReplace, dryRun bool, jobKey string) (broker.WordPressSearchReplaceResult, error) {
	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress || site.Status != "active" {
		return broker.WordPressSearchReplaceResult{}, errors.New("search and replace requires an active WordPress site")
	}
	if err := model.ValidateWordPressSearchReplace(change); err != nil {
		return broker.WordPressSearchReplaceResult{}, err
	}
	if jobKey == "" || len(jobKey) > 160 {
		return broker.WordPressSearchReplaceResult{}, errors.New("invalid WordPress search and replace job key")
	}
	wpcli, ok := h.WordPress.(*WPCLI)
	if !ok || h.Output == nil || !filepath.IsAbs(wpcli.Path) {
		return broker.WordPressSearchReplaceResult{}, errors.New("WordPress search and replace requires WP-CLI")
	}
	if dryRun {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.runWordPressSearchReplace(ctx, site, change, true)
	}
	if err := model.ValidateBackupTarget(target); err != nil {
		return broker.WordPressSearchReplaceResult{}, err
	}

	fingerprint := searchReplaceFingerprint(site.ID, target.ID, change)
	recovery, err := h.openWordPressSearchReplaceRecovery(jobKey)
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, err
	}
	defer recovery.root.Close()
	h.backupMu.Lock()
	defer h.backupMu.Unlock()
	// Inspect the broker-owned marker before contacting backup storage. A
	// completed replay is local and an interrupted apply must fail closed even
	// when its repository is temporarily unavailable.
	h.mu.Lock()
	journal, found, journalErr := recovery.read()
	h.mu.Unlock()
	if journalErr != nil {
		return broker.WordPressSearchReplaceResult{}, journalErr
	}
	if found {
		return existingWordPressSearchReplace(journal, fingerprint)
	}
	recoveryID, err := h.backupSite(ctx, site, target, jobKey+":pre-search-replace")
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, fmt.Errorf("create pre-change recovery point: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	return applyWordPressSearchReplace(recovery, fingerprint, recoveryID, func() (broker.WordPressSearchReplaceResult, error) {
		return h.runWordPressSearchReplace(ctx, site, change, false)
	})
}

func existingWordPressSearchReplace(journal wordpressSearchReplaceJournal, fingerprint string) (broker.WordPressSearchReplaceResult, error) {
	if journal.Fingerprint != fingerprint {
		return broker.WordPressSearchReplaceResult{RecoverySnapshotID: journal.RecoverySnapshotID}, errors.New("search and replace recovery state does not match this operation")
	}
	if journal.Status == "completed" {
		if err := validateCompletedSearchReplaceResult(journal.Result, journal.RecoverySnapshotID); err != nil {
			return broker.WordPressSearchReplaceResult{RecoverySnapshotID: journal.RecoverySnapshotID}, err
		}
		return journal.Result, nil
	}
	return broker.WordPressSearchReplaceResult{RecoverySnapshotID: journal.RecoverySnapshotID}, fmt.Errorf("search and replace requires recovery from snapshot %s before retry", journal.RecoverySnapshotID)
}

func applyWordPressSearchReplace(recovery *wordpressSearchReplaceRecovery, fingerprint, recoveryID string, run func() (broker.WordPressSearchReplaceResult, error)) (broker.WordPressSearchReplaceResult, error) {
	journal, found, err := recovery.read()
	if err != nil {
		return broker.WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, err
	}
	if found {
		if journal.Fingerprint != fingerprint || journal.RecoverySnapshotID != recoveryID {
			return broker.WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, errors.New("search and replace recovery state does not match this operation")
		}
		if journal.Status == "completed" {
			if err := validateCompletedSearchReplaceResult(journal.Result, recoveryID); err != nil {
				return broker.WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, err
			}
			return journal.Result, nil
		}
		return broker.WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, fmt.Errorf("search and replace requires recovery from snapshot %s before retry", recoveryID)
	}
	journal = wordpressSearchReplaceJournal{Fingerprint: fingerprint, RecoverySnapshotID: recoveryID, Status: "started"}
	if err := recovery.write(journal); err != nil {
		return broker.WordPressSearchReplaceResult{RecoverySnapshotID: recoveryID}, err
	}
	result, err := run()
	result.RecoverySnapshotID = recoveryID
	if err != nil {
		return result, fmt.Errorf("search and replace may be partial; recover from snapshot %s: %w", recoveryID, err)
	}
	journal.Status, journal.Result = "completed", result
	if err := recovery.write(journal); err != nil {
		return result, fmt.Errorf("record completed search and replace; recovery snapshot %s is retained: %w", recoveryID, err)
	}
	return result, nil
}

func (h *Host) runWordPressSearchReplace(ctx context.Context, site model.Site, change model.WordPressSearchReplace, dryRun bool) (broker.WordPressSearchReplaceResult, error) {
	publicDir, identity, err := h.wordpressSearchReplaceSite(ctx, site)
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, err
	}
	wpcli := h.WordPress.(*WPCLI)
	runner, ok := h.Output.(boundedOutputRunner)
	if !ok {
		return broker.WordPressSearchReplaceResult{}, errors.New("bounded WP-CLI output is unavailable")
	}
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "--skip-plugins", "--skip-themes"}
	tableOutput, err := runner.OutputBounded(ctx, maxWordPressSearchReplaceOutput, "/usr/sbin/runuser", append(base, "db", "tables", "--all-tables-with-prefix", "--format=csv")...)
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, errors.New("WP-CLI table discovery failed")
	}
	tables, err := parseWordPressPrefixTables(tableOutput)
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, err
	}
	args := append(append([]string{}, base...), "search-replace", change.Search, change.Replace, "--all-tables-with-prefix", "--skip-columns=guid", "--precise", "--format=count")
	if dryRun {
		args = append(args, "--dry-run")
	}
	output, err := runner.OutputBounded(ctx, 128, "/usr/sbin/runuser", args...)
	if err != nil {
		return broker.WordPressSearchReplaceResult{}, errors.New("WP-CLI search and replace failed")
	}
	count, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil || count < 0 {
		return broker.WordPressSearchReplaceResult{}, errors.New("WP-CLI returned an invalid search and replace count")
	}
	if !dryRun {
		if _, err := runner.OutputBounded(ctx, 128, "/usr/sbin/runuser", append(base, "eval", domainClearSiteCache(site.ID))...); err != nil {
			return broker.WordPressSearchReplaceResult{}, errors.New("invalidate site-scoped WordPress object cache")
		}
	}
	return broker.WordPressSearchReplaceResult{Tables: len(tables), Replacements: count}, nil
}

const mathMaxInt64 = int64(^uint64(0) >> 1)

func parseWordPressPrefixTables(output []byte) ([]string, error) {
	if len(output) > maxWordPressSearchReplaceOutput {
		return nil, errors.New("WP-CLI returned an invalid table list")
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	reader.FieldsPerRecord = -1
	result := make([]string, 0)
	seen := make(map[string]struct{})
	fieldIndex := 0
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(record) == 0 {
			return nil, errors.New("WP-CLI returned an invalid table list")
		}
		for _, field := range record {
			name := strings.TrimSpace(field)
			if fieldIndex == 0 && strings.HasPrefix(strings.ToLower(name), "tables_in_") {
				fieldIndex++
				continue
			}
			fieldIndex++
			if !wordpressTableNamePattern.MatchString(name) || len(result) >= maxWordPressSearchReplaceRows {
				return nil, errors.New("WP-CLI returned an invalid table list")
			}
			if _, duplicate := seen[name]; duplicate {
				return nil, errors.New("WP-CLI returned a duplicate table")
			}
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("WP-CLI returned an empty table list")
	}
	return result, nil
}

func (h *Host) wordpressSearchReplaceSite(ctx context.Context, site model.Site) (string, Identity, error) {
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return "", Identity{}, err
	}
	for _, directory := range []string{h.SiteRoot, siteDir, publicDir} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", Identity{}, errors.New("WordPress search and replace requires a trusted site directory")
		}
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	return publicDir, identity, err
}

func searchReplaceFingerprint(siteID, targetID string, change model.WordPressSearchReplace) string {
	encoded, _ := json.Marshal(struct {
		Version  int                          `json:"version"`
		SiteID   string                       `json:"site_id"`
		TargetID string                       `json:"target_id"`
		Change   model.WordPressSearchReplace `json:"change"`
		Scope    string                       `json:"scope"`
	}{1, siteID, targetID, change, "prefix-tables;skip-guid;literal;precise"})
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

func (h *Host) openWordPressSearchReplaceRecovery(jobKey string) (*wordpressSearchReplaceRecovery, error) {
	if !filepath.IsAbs(h.DataRoot) || filepath.Clean(h.DataRoot) != h.DataRoot || h.DataRoot == "/" {
		return nil, errors.New("search and replace data root must be absolute, clean, and non-root")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(jobKey)))
	root, err := os.OpenRoot(h.DataRoot)
	if err != nil {
		return nil, err
	}
	for _, component := range []string{"wordpress-search-replace", digest} {
		created := false
		if err := root.Mkdir(component, 0700); err == nil {
			created = true
		} else if !os.IsExist(err) {
			root.Close()
			return nil, err
		}
		if created {
			if err := syncWordPressSearchReplaceDirectory(root); err != nil {
				root.Close()
				return nil, err
			}
		}
		info, err := root.Lstat(component)
		if err != nil || !info.IsDir() || !domainOwnedByBroker(info) || info.Mode().Perm() != 0700 {
			root.Close()
			return nil, errors.New("search and replace recovery directory must be private and broker-owned")
		}
		child, err := root.OpenRoot(component)
		root.Close()
		if err != nil {
			return nil, err
		}
		pinned, err := child.Stat(".")
		if err != nil || !os.SameFile(info, pinned) {
			child.Close()
			return nil, errors.New("search and replace recovery directory changed while opening")
		}
		root = child
	}
	return &wordpressSearchReplaceRecovery{root: root}, nil
}

func syncWordPressSearchReplaceDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (recovery *wordpressSearchReplaceRecovery) read() (wordpressSearchReplaceJournal, bool, error) {
	file, err := recovery.root.OpenFile("state.json", os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return wordpressSearchReplaceJournal{}, false, nil
	}
	if err != nil {
		return wordpressSearchReplaceJournal{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !domainOwnedByBroker(info) || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
		return wordpressSearchReplaceJournal{}, true, errors.New("search and replace recovery file must be private and broker-owned")
	}
	var journal wordpressSearchReplaceJournal
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || jsonDecoderHasTrailing(decoder) || (journal.Status != "started" && journal.Status != "completed") || !model.ValidResticSnapshotID(journal.RecoverySnapshotID) || !wordpressSearchReplaceFingerprintPattern.MatchString(journal.Fingerprint) {
		return wordpressSearchReplaceJournal{}, true, errors.New("search and replace recovery state is invalid")
	}
	return journal, true, nil
}

func jsonDecoderHasTrailing(decoder *json.Decoder) bool {
	var trailing any
	return !errors.Is(decoder.Decode(&trailing), io.EOF)
}

func (recovery *wordpressSearchReplaceRecovery) write(journal wordpressSearchReplaceJournal) error {
	content, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	random, err := randomPassword()
	if err != nil {
		return err
	}
	name := ".state-" + random
	file, err := recovery.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer recovery.root.Remove(name)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := recovery.root.Rename(name, "state.json"); err != nil {
		return err
	}
	directory, err := recovery.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validateCompletedSearchReplaceResult(result broker.WordPressSearchReplaceResult, recoveryID string) error {
	if result.RecoverySnapshotID != recoveryID || !model.ValidResticSnapshotID(recoveryID) || result.Tables < 0 || result.Tables > maxWordPressSearchReplaceRows || result.Replacements < 0 {
		return errors.New("completed search and replace recovery result is invalid")
	}
	if len(result.TableResults) == 0 {
		return nil
	}
	if result.Tables != len(result.TableResults) {
		return errors.New("completed search and replace recovery result is invalid")
	}
	seen := make(map[string]struct{}, len(result.TableResults))
	var total int64
	for _, table := range result.TableResults {
		if !wordpressTableNamePattern.MatchString(table.Name) || table.Replacements < 0 || table.Replacements > mathMaxInt64-total {
			return errors.New("completed search and replace recovery result is invalid")
		}
		if _, duplicate := seen[table.Name]; duplicate {
			return errors.New("completed search and replace recovery result is invalid")
		}
		seen[table.Name] = struct{}{}
		total += table.Replacements
	}
	if total != result.Replacements {
		return errors.New("completed search and replace recovery result is invalid")
	}
	return nil
}
