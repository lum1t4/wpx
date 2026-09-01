package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

var pluginSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

func (h *Host) Inventory(ctx context.Context, site model.Site) (broker.WordPressInventoryResult, error) {
	plugins, err := h.Plugins(ctx, site)
	if err != nil {
		return broker.WordPressInventoryResult{}, err
	}
	themeOutput, err := h.runWordPressOutput(ctx, site, "theme", "list", "--skip-update-check", "--format=json", "--fields=name,status,version,update,update_version")
	if err != nil {
		return broker.WordPressInventoryResult{}, err
	}
	var themes []broker.WordPressTheme
	if err := json.Unmarshal(themeOutput, &themes); err != nil {
		return broker.WordPressInventoryResult{}, fmt.Errorf("decode WP-CLI theme inventory: %w", err)
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
	var plugins []broker.WordPressPlugin
	if err := json.Unmarshal(output, &plugins); err != nil {
		return nil, fmt.Errorf("decode WP-CLI plugin inventory: %w", err)
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
	if err := model.ValidateSite(site); err != nil || site.Kind != model.WordPress {
		return nil, errors.New("operation requires a valid WordPress site")
	}
	if h.Output == nil {
		return nil, errors.New("command output runner is unavailable")
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	publicDir := filepath.Join(siteDir, "public")
	if err := ensureContained(h.SiteRoot, publicDir); err != nil {
		return nil, err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return nil, err
	}
	base := []string{"--user", identity.Name, "--", "/usr/bin/php" + site.PHPVersion, "/usr/local/lib/wpx/wp-cli.phar", "--path=" + publicDir, "--no-color"}
	return h.Output.Output(ctx, "/usr/sbin/runuser", append(base, args...)...)
}
