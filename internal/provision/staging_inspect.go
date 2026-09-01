package provision

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

const maxStagingInspectionFiles = 10000

type stagingFingerprint struct {
	size int64
	hash [sha256.Size]byte
}

// InspectStaging presents only deployable differences. Credentials, temporary
// login scripts, and WPX's staging guard are control-plane state and can never
// become selectable production content.
func (h *Host) InspectStaging(ctx context.Context, staging, production model.Site) (broker.StagingInspection, error) {
	if err := validateStagingPair(staging, production); err != nil {
		return broker.StagingInspection{}, err
	}
	stagingPublic := filepath.Join(h.SiteRoot, staging.ID, "public")
	productionPublic := filepath.Join(h.SiteRoot, production.ID, "public")
	stagingFiles, err := fingerprintTree(stagingPublic)
	if err != nil {
		return broker.StagingInspection{}, fmt.Errorf("inspect staging files: %w", err)
	}
	productionFiles, err := fingerprintTree(productionPublic)
	if err != nil {
		return broker.StagingInspection{}, fmt.Errorf("inspect production files: %w", err)
	}
	paths := make(map[string]struct{}, len(stagingFiles)+len(productionFiles))
	for name := range stagingFiles {
		paths[name] = struct{}{}
	}
	for name := range productionFiles {
		paths[name] = struct{}{}
	}
	changes := make([]broker.StagingFileChange, 0)
	for name := range paths {
		staged, inStaging := stagingFiles[name]
		live, inProduction := productionFiles[name]
		status := ""
		size := int64(0)
		switch {
		case inStaging && !inProduction:
			status, size = "added", staged.size
		case !inStaging && inProduction:
			status, size = "deleted", live.size
		case staged != live:
			status, size = "modified", staged.size
		}
		if status != "" {
			changes = append(changes, broker.StagingFileChange{Path: name, Status: status, Bytes: size})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	tables, err := h.wordpressTables(ctx, staging, stagingPublic)
	if err != nil {
		return broker.StagingInspection{}, err
	}
	return broker.StagingInspection{Files: changes, Tables: tables}, nil
}

func validateStagingPair(staging, production model.Site) error {
	if model.ValidateSite(staging) != nil || model.ValidateSite(production) != nil || staging.Kind != model.WordPress || staging.Environment != "staging" || staging.Status != "active" || staging.ParentSiteID != production.ID || staging.WordPressMultisite != production.WordPressMultisite || production.Kind != model.WordPress || production.Environment != "production" || production.Status != "active" {
		return errors.New("invalid staging-to-production relationship")
	}
	return nil
}

func fingerprintTree(root string) (map[string]stagingFingerprint, error) {
	result := make(map[string]stagingFingerprint)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if excludedStagingPath(relative) {
			return nil
		}
		if len(result) >= maxStagingInspectionFiles {
			return errors.New("site has too many files for custom deployment")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var hash [sha256.Size]byte
		copy(hash[:], digest.Sum(nil))
		result[relative] = stagingFingerprint{size: info.Size(), hash: hash}
		return nil
	})
	return result, err
}

func excludedStagingPath(relative string) bool {
	base := filepath.Base(relative)
	return relative == "wp-config.php" || relative == "wp-content/mu-plugins/wpx-staging.php" || strings.HasPrefix(base, "wpx-login-") && strings.HasSuffix(base, ".php")
}

func (h *Host) wordpressTables(ctx context.Context, site model.Site, publicDir string) ([]string, error) {
	wpcli, ok := h.WordPress.(*WPCLI)
	if !ok || h.Output == nil || !filepath.IsAbs(wpcli.Path) {
		return nil, errors.New("staging inspection requires WP-CLI output support")
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return nil, err
	}
	arguments := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, wpcli.Path, "--path=" + publicDir, "--no-color", "db", "tables", "--all-tables-with-prefix", "--format=json"}
	output, err := h.Output.Output(ctx, "/usr/sbin/runuser", arguments...)
	if err != nil {
		return nil, fmt.Errorf("list staging database tables: %w", err)
	}
	var tables []string
	if err := json.Unmarshal(output, &tables); err != nil {
		return nil, errors.New("WP-CLI returned an invalid database table list")
	}
	for _, table := range tables {
		if !databaseTableName(table) {
			return nil, errors.New("WP-CLI returned an unsafe database table name")
		}
	}
	sort.Strings(tables)
	return tables, nil
}

func databaseTableName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
