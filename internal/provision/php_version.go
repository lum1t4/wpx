package provision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

// ChangePHPVersion moves only this site's FPM pool. The stable socket keeps its
// Nginx configuration, TLS, staging protection and application data unchanged.
// Releasing that socket before the new FPM branch starts causes a short pause.
func (h *Host) ChangePHPVersion(ctx context.Context, site model.Site, change model.PHPVersionChange) error {
	if err := model.ValidatePHPVersionChange(site, change); err != nil {
		return err
	}
	if site.Status != "php_changing" {
		return errors.New("site is not awaiting a PHP version change")
	}
	if err := h.validate(); err != nil {
		return err
	}
	php, ok := h.PHP.(*AptPHPRuntime)
	if !ok || php.Runner == nil {
		return errors.New("PHP version manager is unavailable")
	}
	for _, path := range []string{php.ConfigRoot, php.RunRoot, php.SnippetRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("PHP runtime paths must be absolute, clean and non-root")
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	oldPool := filepath.Join(php.ConfigRoot, change.PreviousVersion, "fpm", "pool.d", "wpx-"+site.ID+".conf")
	newPool := filepath.Join(php.ConfigRoot, change.Version, "fpm", "pool.d", "wpx-"+site.ID+".conf")
	oldContent, oldExists, err := managedFileStateWithMarker(oldPool, phpOwnershipMarker)
	if err != nil {
		return fmt.Errorf("inspect current PHP pool: %w", err)
	}
	if _, _, err := managedFileStateWithMarker(newPool, phpOwnershipMarker); err != nil {
		return fmt.Errorf("inspect target PHP pool: %w", err)
	}
	// The backup is outside FPM's *.conf include. Keep it after success: a worker
	// can die after the host switch but before committing its new version to
	// SQLite. Replaying that job can still recover the declared previous pool.
	recoveryPath := oldPool + ".wpx-previous"
	recoveryContent, recoveryExists, err := managedFileStateWithMarker(recoveryPath, phpOwnershipMarker)
	if err != nil {
		return fmt.Errorf("inspect PHP recovery pool: %w", err)
	}
	if !oldExists {
		if !recoveryExists {
			return errors.New("current PHP pool and its recovery copy are missing")
		}
		oldContent = recoveryContent
	}
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	if err := ensureContained(h.SiteRoot, siteDir); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	unchangedFailure := func(cause error) error {
		if oldExists {
			serviceErr := php.Runner.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "php"+change.PreviousVersion+"-fpm.service")
			if serviceErr == nil && php.waitReady(ctx, filepath.Join(php.RunRoot, "wpx-"+site.ID+".sock")) == nil {
				return &model.PHPVersionChangeError{Err: cause, PreviousRestored: true}
			}
		}
		return cause
	}
	// Download and install the selected branch while the current site still
	// serves traffic. No other PHP branch is installed by this operation.
	if err := php.ensurePackages(ctx, change.Version); err != nil {
		return unchangedFailure(err)
	}
	if oldExists {
		if err := atomicWrite(recoveryPath, oldContent, 0600); err != nil {
			return unchangedFailure(fmt.Errorf("save previous PHP pool: %w", err))
		}
		if err := os.Remove(oldPool); err != nil {
			return unchangedFailure(fmt.Errorf("release previous PHP pool: %w", err))
		}
	}
	rollback := func(cause error) error {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		// Stop listening on the shared socket before bringing its old owner
		// back. On failure keep the explicit recovery path in the job error.
		if err := os.Remove(newPool); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w; rollback cannot remove target pool: %v; recover from %s", cause, err, recoveryPath)
		}
		if err := php.reloadOrStopUnused(recoveryCtx, change.Version); err != nil {
			return fmt.Errorf("%w; rollback cannot release target runtime: %v; recover from %s", cause, err, recoveryPath)
		}
		if err := atomicWrite(oldPool, oldContent, 0644); err != nil {
			return fmt.Errorf("%w; rollback cannot restore previous pool: %v; recover from %s", cause, err, recoveryPath)
		}
		service := "php" + change.PreviousVersion + "-fpm.service"
		for _, command := range [][]string{
			{"/usr/sbin/php-fpm" + change.PreviousVersion, "-t"},
			{"/usr/bin/systemctl", "enable", "--now", service},
			{"/usr/bin/systemctl", "reload", service},
			{"/usr/bin/systemctl", "is-active", "--quiet", service},
		} {
			if err := php.Runner.Run(recoveryCtx, command[0], command[1:]...); err != nil {
				return fmt.Errorf("%w; previous pool restored but runtime restart failed: %v; recover from %s", cause, err, recoveryPath)
			}
		}
		if err := php.waitReady(recoveryCtx, filepath.Join(php.RunRoot, "wpx-"+site.ID+".sock")); err != nil {
			return fmt.Errorf("%w; previous pool restored but runtime is not ready: %v; recover from %s", cause, err, recoveryPath)
		}
		return &model.PHPVersionChangeError{Err: fmt.Errorf("%w; previous PHP %s restored", cause, change.PreviousVersion), PreviousRestored: true}
	}
	if err := php.reloadOrStopUnused(ctx, change.PreviousVersion); err != nil {
		return rollback(fmt.Errorf("release PHP %s: %w", change.PreviousVersion, err))
	}
	target := site
	target.PHPVersion, target.AllowEOL = change.Version, change.AllowEOL
	if _, err := php.Ensure(ctx, target, identity, siteDir); err != nil {
		return rollback(err)
	}
	if err := php.Runner.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "php"+change.Version+"-fpm.service"); err != nil {
		return rollback(fmt.Errorf("new PHP runtime did not become active: %w", err))
	}
	if err := php.waitReady(ctx, filepath.Join(php.RunRoot, "wpx-"+site.ID+".sock")); err != nil {
		return rollback(err)
	}
	if err := h.Runner.Run(ctx, "/usr/sbin/nginx", "-t"); err != nil {
		return rollback(fmt.Errorf("validate Nginx after PHP change: %w", err))
	}
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "reload", "nginx.service"); err != nil {
		return rollback(fmt.Errorf("reload Nginx after PHP change: %w", err))
	}
	return nil
}

func (p *AptPHPRuntime) reloadOrStopUnused(ctx context.Context, version string) error {
	poolDir := filepath.Join(p.ConfigRoot, version, "fpm", "pool.d")
	entries, err := os.ReadDir(poolDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect remaining PHP pools: %w", err)
	}
	inUse := false
	for _, entry := range entries {
		// WPX never routes a site to the distribution's default www pool.
		// Other configured pools, including expert-created ones, keep their
		// service running regardless of a possibly stale database status.
		if strings.HasSuffix(entry.Name(), ".conf") && entry.Name() != "www.conf" {
			inUse = true
		}
	}
	service := "php" + version + "-fpm.service"
	if !inUse {
		return p.Runner.Run(ctx, "/usr/bin/systemctl", "disable", "--now", service)
	}
	if err := p.Runner.Run(ctx, "/usr/sbin/php-fpm"+version, "-t"); err != nil {
		return err
	}
	return p.Runner.Run(ctx, "/usr/bin/systemctl", "reload", service)
}
