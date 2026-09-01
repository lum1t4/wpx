package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

func ensureManagedSnippetFile(root, siteID, marker string) (string, error) {
	if !filepath.IsAbs(root) || root == "/" || model.ValidateSiteID(siteID) != nil {
		return "", errors.New("invalid snippet path")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(root, siteID+".conf")
	content, exists, err := managedFileStateWithMarker(path, marker)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := atomicWrite(path, []byte(marker), 0644); err != nil {
			return "", err
		}
	} else if marker == phpOwnershipMarker && strings.HasPrefix(string(content), ownershipMarker) {
		content = append([]byte(marker), content[len(ownershipMarker):]...)
		if err := atomicWrite(path, content, 0644); err != nil {
			return "", err
		}
	}
	return path, nil
}

func (h *Host) ensureNginxSnippet(siteID string) (string, error) {
	return ensureManagedSnippetFile(h.NginxSnippetRoot, siteID, ownershipMarker)
}

func injectNginxSnippet(configuration string, site model.Site, snippetPath string) (string, error) {
	needle := nginxLogDirectives(site)
	if !strings.Contains(configuration, needle) || !filepath.IsAbs(snippetPath) {
		return "", errors.New("generated Nginx configuration cannot accept expert snippet")
	}
	return strings.Replace(configuration, needle, needle+"    include "+snippetPath+";\n", 1), nil
}

func (h *Host) ApplySnippets(ctx context.Context, site model.Site, snippets model.SiteSnippets) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := model.ValidateSiteSnippets(site, snippets); err != nil {
		return err
	}
	if site.Status != "active" && site.Status != "disabled" {
		return errors.New("site must be active or disabled to apply snippets")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	nginxPath, err := ensureManagedSnippetFile(h.NginxSnippetRoot, site.ID, ownershipMarker)
	if err != nil {
		return err
	}
	phpPath, err := ensureManagedSnippetFile(h.PHPSnippetRoot, site.ID, phpOwnershipMarker)
	if err != nil {
		return err
	}
	previousNginx, _, err := managedFileState(nginxPath)
	if err != nil {
		return err
	}
	previousPHP, _, err := managedFileStateWithMarker(phpPath, phpOwnershipMarker)
	if err != nil {
		return err
	}
	content := func(marker, value string) []byte {
		value = strings.TrimSpace(value)
		if value == "" {
			return []byte(marker)
		}
		return []byte(marker + value + "\n")
	}
	if err := atomicWrite(nginxPath, content(ownershipMarker, snippets.Nginx), 0644); err != nil {
		return err
	}
	if err := atomicWrite(phpPath, content(phpOwnershipMarker, snippets.PHP), 0644); err != nil {
		_ = atomicWrite(nginxPath, previousNginx, 0644)
		return err
	}
	rollback := func() {
		_ = atomicWrite(nginxPath, previousNginx, 0644)
		_ = atomicWrite(phpPath, previousPHP, 0644)
	}
	if site.Status == "disabled" {
		return nil
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		rollback()
		return fmt.Errorf("validate Nginx expert snippet: %w", err)
	}
	phpSite := site.Kind == model.WordPress || site.Kind == model.PHP
	if phpSite {
		if err := h.Runner.Run(ctx, "/usr/sbin/php-fpm"+site.PHPVersion, "-t"); err != nil {
			rollback()
			return fmt.Errorf("validate PHP expert snippet: %w", err)
		}
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		rollback()
		return fmt.Errorf("reload Nginx expert snippet: %w", err)
	}
	if phpSite {
		if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "php"+site.PHPVersion+"-fpm.service"); err != nil {
			rollback()
			_ = h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service")
			return fmt.Errorf("reload PHP expert snippet: %w", err)
		}
	}
	return nil
}
