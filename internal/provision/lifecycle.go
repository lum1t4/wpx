package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lum1t4/wpx/internal/model"
)

// Disable removes traffic and runtime activation without deleting site data.
// Available Nginx configuration, files, databases, certificates, and backups
// remain intact so Enable can converge through ordinary provisioning.
func (h *Host) Disable(ctx context.Context, site model.Site, stopPHP bool) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if site.Status != "disabling" && site.Status != "disable_failed" {
		return errors.New("site is not awaiting disable")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	removedLinks := make([]struct{ enabled, available string }, 0, 2)
	for _, name := range []string{"wpx-" + site.ID + ".conf", "wpx-" + site.ID + "-tls.conf"} {
		available := filepath.Join(h.NginxAvailable, name)
		enabled := filepath.Join(h.NginxEnabled, name)
		target, err := os.Readlink(enabled)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || target != available {
			return fmt.Errorf("refuse to disable unmanaged Nginx path %s", enabled)
		}
		if err := os.Remove(enabled); err != nil {
			return err
		}
		removedLinks = append(removedLinks, struct{ enabled, available string }{enabled, available})
	}
	rollbackLinks := func() {
		for _, link := range removedLinks {
			_ = os.Symlink(link.available, link.enabled)
		}
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		rollbackLinks()
		return fmt.Errorf("validate Nginx after disabling site: %w", err)
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		rollbackLinks()
		return fmt.Errorf("reload Nginx after disabling site: %w", err)
	}
	rollbackTraffic := func() {
		rollbackLinks()
		_ = h.Runner.Run(context.Background(), "/usr/sbin/nginx", "-t")
		_ = h.Runner.Run(context.Background(), "/usr/bin/systemctl", "reload", "nginx.service")
	}
	switch site.Kind {
	case model.WordPress, model.PHP:
		php, ok := h.PHP.(*AptPHPRuntime)
		if !ok {
			rollbackTraffic()
			return errors.New("PHP lifecycle manager is unavailable")
		}
		poolPath := filepath.Join(php.ConfigRoot, site.PHPVersion, "fpm", "pool.d", "wpx-"+site.ID+".conf")
		previousPool, exists, err := managedFileStateWithMarker(poolPath, phpOwnershipMarker)
		if err != nil {
			rollbackTraffic()
			return fmt.Errorf("inspect PHP pool before disable: %w", err)
		}
		if exists {
			if err := os.Remove(poolPath); err != nil {
				rollbackTraffic()
				return fmt.Errorf("remove PHP pool: %w", err)
			}
		}
		restorePoolAndTraffic := func() {
			if exists {
				_ = atomicWrite(poolPath, previousPool, 0644)
			}
			rollbackTraffic()
		}
		service := "php" + site.PHPVersion + "-fpm.service"
		if stopPHP {
			if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "stop", service); err != nil {
				restorePoolAndTraffic()
				return fmt.Errorf("stop unused PHP %s: %w", site.PHPVersion, err)
			}
		} else if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", service); err != nil {
			restorePoolAndTraffic()
			return fmt.Errorf("reload PHP %s after removing pool: %w", site.PHPVersion, err)
		}
	case model.Python:
		unitBase := "wpx-python-" + site.ID
		if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "disable", "--now", unitBase+".socket"); err != nil {
			return fmt.Errorf("stop Python socket: %w", err)
		}
		if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "stop", unitBase+".service"); err != nil {
			return fmt.Errorf("stop Python workers: %w", err)
		}
	}
	return nil
}
