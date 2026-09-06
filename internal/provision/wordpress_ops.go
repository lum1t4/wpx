package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

var pluginSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// WP-CLI uses a string update status for ordinary plugins, but boolean false
// for drop-ins such as Redis's object-cache.php. Normalize that external shape
// here so one drop-in cannot invalidate the inventory, and the broker protocol
// keeps a single string representation for all consumers.
type wpCLIUpdateState string

func (state *wpCLIUpdateState) UnmarshalJSON(data []byte) error {
	if string(data) == "false" {
		*state = "none"
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*state = wpCLIUpdateState(value)
	return nil
}

type wpCLIInventoryItem struct {
	Name          string           `json:"name"`
	Status        string           `json:"status"`
	Version       string           `json:"version"`
	Update        wpCLIUpdateState `json:"update"`
	UpdateVersion string           `json:"update_version"`
}

func (h *Host) Inventory(ctx context.Context, site model.Site) (broker.WordPressInventoryResult, error) {
	plugins, err := h.Plugins(ctx, site)
	if err != nil {
		return broker.WordPressInventoryResult{}, err
	}
	themeOutput, err := h.runWordPressOutput(ctx, site, "theme", "list", "--skip-update-check", "--format=json", "--fields=name,status,version,update,update_version")
	if err != nil {
		return broker.WordPressInventoryResult{}, err
	}
	var themeItems []wpCLIInventoryItem
	if err := json.Unmarshal(themeOutput, &themeItems); err != nil {
		return broker.WordPressInventoryResult{}, fmt.Errorf("decode WP-CLI theme inventory: %w", err)
	}
	themes := make([]broker.WordPressTheme, 0, len(themeItems))
	for _, item := range themeItems {
		themes = append(themes, broker.WordPressTheme{
			Name: item.Name, Status: item.Status, Version: item.Version,
			Update: string(item.Update), UpdateVersion: item.UpdateVersion,
		})
	}
	versionOutput, err := h.runWordPressOutput(ctx, site, "core", "version")
	if err != nil {
		return broker.WordPressInventoryResult{}, err
	}
	version := strings.TrimSpace(string(versionOutput))
	if version == "" || len(version) > 32 {
		return broker.WordPressInventoryResult{}, errors.New("WP-CLI returned an invalid core version")
	}
	result := broker.WordPressInventoryResult{CoreVersion: version, Plugins: plugins, Themes: themes}
	updateOutput, err := h.runWordPressOutput(ctx, site, "core", "check-update", "--format=json")
	if err == nil {
		var updates []struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(updateOutput, &updates) == nil && len(updates) != 0 && len(updates[0].Version) <= 32 {
			result.CoreUpdateVersion = updates[0].Version
		}
	}
	return result, nil
}

func (h *Host) Health(ctx context.Context, site model.Site) broker.WordPressHealthResult {
	commands := []struct {
		name string
		args []string
	}{
		{name: "WordPress loads", args: []string{"core", "is-installed"}},
		{name: "Core files match WordPress.org", args: []string{"core", "verify-checksums"}},
		{name: "Database tables pass checks", args: []string{"db", "check"}},
	}
	result := broker.WordPressHealthResult{Checks: make([]broker.WordPressHealthCheck, 0, len(commands))}
	for _, check := range commands {
		status := "healthy"
		if _, err := h.runWordPressOutput(ctx, site, check.args...); err != nil {
			status = "failed"
		}
		result.Checks = append(result.Checks, broker.WordPressHealthCheck{Name: check.name, Status: status})
	}
	return result
}

func (h *Host) Plugins(ctx context.Context, site model.Site) ([]broker.WordPressPlugin, error) {
	output, err := h.runWordPressOutput(ctx, site, "plugin", "list", "--skip-update-check", "--format=json", "--fields=name,status,version,update,update_version")
	if err != nil {
		return nil, err
	}
	var items []wpCLIInventoryItem
	if err := json.Unmarshal(output, &items); err != nil {
		return nil, fmt.Errorf("decode WP-CLI plugin inventory: %w", err)
	}
	plugins := make([]broker.WordPressPlugin, 0, len(items))
	for _, item := range items {
		plugins = append(plugins, broker.WordPressPlugin{
			Name: item.Name, Status: item.Status, Version: item.Version,
			Update: string(item.Update), UpdateVersion: item.UpdateVersion,
		})
	}
	return plugins, nil
}

func (h *Host) SetPlugin(ctx context.Context, site model.Site, plugin string, active bool) error {
	if !pluginSlugPattern.MatchString(plugin) {
		return errors.New("invalid plugin slug")
	}
	action := "deactivate"
	if active {
		action = "activate"
	}
	_, err := h.runWordPressOutput(ctx, site, "plugin", action, plugin)
	return err
}

func (h *Host) runWordPressOutput(ctx context.Context, site model.Site, args ...string) ([]byte, error) {
	// Lifecycle work can rewrite or restore the WordPress database. Serialize
	// each command with that work; the inventory itself need not hold the lock
	// across all of its independent reads.
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress {
		return nil, errors.New("operation requires a valid WordPress site")
	}
	if err := h.validate(); err != nil {
		return nil, err
	}
	if h.Output == nil {
		return nil, errors.New("command output runner is unavailable")
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return nil, err
	}
	// A request authorized before deletion must not recreate the removed Unix
	// user. Reject missing trees and symlinked site components before Ensure.
	for _, directory := range []string{h.SiteRoot, siteDir, publicDir} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("WordPress operation requires a real directory at %s", directory)
		}
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return nil, err
	}
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, "/usr/local/lib/wpx/wp-cli.phar", "--path=" + publicDir, "--no-color"}
	return h.Output.Output(ctx, "/usr/sbin/runuser", append(base, args...)...)
}
