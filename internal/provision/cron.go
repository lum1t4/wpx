package provision

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
)

const cronMarker = "# Managed by WPX. Manual changes will be replaced.\n"
const wpCronMarker = "/* Managed by WPX: external cron. */ define('DISABLE_WP_CRON', true);"

var wpCronDefine = regexp.MustCompile(`(?m)^\s*(?:/\* Managed by WPX: external cron\. \*/\s*)?define\(\s*['\"]DISABLE_WP_CRON['\"]\s*,[^;]+;\s*$`)

func (h *Host) ApplyCronSchedule(ctx context.Context, site model.Site, schedule model.CronSchedule, remove bool) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := model.ValidateCronSchedule(schedule); err != nil {
		return err
	}
	if schedule.SiteID != site.ID {
		return errors.New("cron schedule belongs to another site")
	}
	if site.Status != "active" && site.Status != "disabled" {
		return errors.New("site must be active or disabled")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	name := cronFileName(site.ID, schedule.ID)
	source := filepath.Join(h.DataRoot, "cron", name)
	if remove {
		if err := secureSetCronTarget(h.CronRoot, name, nil, false); err != nil {
			return err
		}
		if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	identity, err := h.Identities.Ensure(ctx, site, filepath.Join(h.SiteRoot, site.ID))
	if err != nil {
		return err
	}
	if err := ensureCronStateDirectory(filepath.Dir(source)); err != nil {
		return fmt.Errorf("prepare cron state directory: %w", err)
	}
	content := renderCronFile(schedule.Expression, identity.Name, schedule.Command)
	if err := atomicWrite(source, []byte(content), 0600); err != nil {
		return fmt.Errorf("save cron source: %w", err)
	}
	if !schedule.Enabled {
		if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
			return err
		}
		return secureSetCronTarget(h.CronRoot, name, nil, false)
	}
	if site.Status == "disabled" {
		return secureSetCronTarget(h.CronRoot, name, nil, false)
	}
	if err := h.requireCronService(ctx); err != nil {
		return err
	}
	return secureSetCronTarget(h.CronRoot, name, []byte(content), true)
}

func (h *Host) ApplyWordPressCronReplacement(ctx context.Context, site model.Site, setting model.WordPressCronSetting) error {
	if err := model.ValidateSite(site); err != nil {
		return err
	}
	if err := model.ValidateWordPressCronSetting(setting); err != nil {
		return err
	}
	if site.Kind != model.WordPress || setting.SiteID != site.ID {
		return errors.New("WordPress cron setting belongs to another site")
	}
	if site.Status != "active" && site.Status != "disabled" {
		return errors.New("site must be active or disabled")
	}
	if err := h.validate(); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	siteDir := filepath.Join(h.SiteRoot, site.ID)
	configPath := filepath.Join(siteDir, "public", "wp-config.php")
	if err := ensureContained(h.SiteRoot, configPath); err != nil {
		return err
	}
	identity, err := h.Identities.Ensure(ctx, site, siteDir)
	if err != nil {
		return err
	}
	name := cronFileName(site.ID, "wordpress")
	source := filepath.Join(h.DataRoot, "cron", name)
	wp, ok := h.WordPress.(*WPCLI)
	if !ok || !filepath.IsAbs(wp.Path) {
		return errors.New("WordPress cron requires the managed WP-CLI runtime")
	}
	command := []string{"/usr/bin/php" + site.PHPVersion, wp.Path, "--path=" + filepath.Join(siteDir, "public"), "--no-color", "cron", "event", "run", "--due-now", "--quiet"}
	if setting.Replaced {
		if err := ensureCronStateDirectory(filepath.Dir(source)); err != nil {
			return fmt.Errorf("prepare cron state directory: %w", err)
		}
		if err := atomicWrite(source, []byte(renderCronFile(setting.Expression, identity.Name, command)), 0600); err != nil {
			return fmt.Errorf("save WordPress cron source: %w", err)
		}
		installed := false
		if site.Status == "active" {
			if err := h.requireCronService(ctx); err != nil {
				return err
			}
			if err := secureSetCronTarget(h.CronRoot, name, []byte(renderCronFile(setting.Expression, identity.Name, command)), true); err != nil {
				return fmt.Errorf("install WordPress cron: %w", err)
			}
			installed = true
		}
		if err := secureSetWPInternalCron(h, site, configPath, true, identity); err != nil {
			if installed {
				_ = secureSetCronTarget(h.CronRoot, name, nil, false)
			}
			return err
		}
		return nil
	}
	// Restore WordPress's request-driven runner before removing the external
	// trigger. An interruption can briefly run both, but cannot leave no runner.
	if err := secureSetWPInternalCron(h, site, configPath, false, identity); err != nil {
		return err
	}
	if err := secureSetCronTarget(h.CronRoot, name, nil, false); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (h *Host) requireCronService(ctx context.Context) error {
	if err := h.Runner.Run(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "cron.service"); err != nil {
		return fmt.Errorf("cron.service is unavailable or inactive; install and start cron before enabling schedules: %w", err)
	}
	return nil
}

// SetSiteCronEnabled is called by site lifecycle reconciliation. Desired files
// remain under DataRoot while a disabled site has no live /etc/cron.d entries.
func (h *Host) SetSiteCronEnabled(ctx context.Context, site model.Site, enabled bool) error {
	prefix := cronSitePrefix(site.ID)
	directory := filepath.Join(h.DataRoot, "cron")
	entries, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		source := filepath.Join(directory, entry.Name())
		if err := inspectCronSource(source); err != nil {
			return err
		}
		if enabled {
			content, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			if err := secureSetCronTarget(h.CronRoot, entry.Name(), content, true); err != nil {
				return err
			}
		} else if err := secureSetCronTarget(h.CronRoot, entry.Name(), nil, false); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) RemoveSiteCron(ctx context.Context, site model.Site) error {
	if err := h.SetSiteCronEnabled(ctx, site, false); err != nil {
		return err
	}
	directory, prefix := filepath.Join(h.DataRoot, "cron"), cronSitePrefix(site.ID)
	entries, err := os.ReadDir(directory)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func cronSitePrefix(siteID string) string {
	digest := sha256.Sum256([]byte(siteID))
	return fmt.Sprintf("wpx-%x-", digest[:6])
}

func cronFileName(siteID, scheduleID string) string {
	digest := sha256.Sum256([]byte(scheduleID))
	return fmt.Sprintf("%s%x", cronSitePrefix(siteID), digest[:6])
}

func inspectCronTarget(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed cron target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refuse non-regular managed cron target")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read managed cron target: %w", err)
	}
	if !strings.HasPrefix(string(content), cronMarker) {
		return errors.New("refuse to replace unmanaged cron target")
	}
	return nil
}

func secureSetCronTarget(rootPath, name string, content []byte, install bool) error {
	root, err := openSecureCronRoot(rootPath)
	if err != nil {
		return fmt.Errorf("open cron root safely: %w", err)
	}
	defer root.Close()
	entry, err := root.Lstat(name)
	if err == nil && entry.Mode()&os.ModeSymlink != 0 {
		return errors.New("refuse symbolic-link managed cron target")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing, err := root.Open(name)
	if err == nil {
		info, statErr := existing.Stat()
		stored, readErr := io.ReadAll(io.LimitReader(existing, 1<<20))
		existing.Close()
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.New("refuse non-regular managed cron target")
		}
		if readErr != nil || !strings.HasPrefix(string(stored), cronMarker) {
			return errors.New("refuse to replace unmanaged cron target")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect managed cron target: %w", err)
	}
	if !install {
		if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	temporary := ".wpx-cron-" + strings.TrimPrefix(cronFileName(name, name), "wpx-")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = root.Remove(temporary)
		return err
	}
	if closeErr != nil {
		_ = root.Remove(temporary)
		return closeErr
	}
	if err := root.Rename(temporary, name); err != nil {
		_ = root.Remove(temporary)
		return err
	}
	return nil
}

func inspectCronSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect retained cron source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("retained cron source is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(content), cronMarker) {
		return errors.New("retained cron source lacks the WPX ownership marker")
	}
	return nil
}

func ensureCronStateDirectory(path string) error {
	if err := os.MkdirAll(path, 0750); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cron state path must be a real directory")
	}
	return os.Chmod(path, 0750)
}

func renderCronFile(expression, username string, command []string) string {
	quoted := make([]string, len(command))
	for i, argument := range command {
		quoted[i] = cronQuote(argument)
	}
	return cronMarker + "SHELL=/bin/sh\nPATH=/usr/local/bin:/usr/bin:/bin\n\n" + expression + " " + username + " " + strings.Join(quoted, " ") + "\n"
}

func cronQuote(value string) string {
	// Cron consumes percent signs before invoking its configured shell, even
	// inside quotes. Escape them at the crontab layer, then quote for the shell.
	value = strings.ReplaceAll(value, "%", `\%`)
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func setWPInternalCron(path string, disable bool, identity Identity) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect wp-config.php: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("wp-config.php must be a regular file, not a symbolic link")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read wp-config.php: %w", err)
	}
	match := wpCronDefine.Find(content)
	if disable {
		if match != nil {
			if string(match) == wpCronMarker {
				return nil
			}
			return errors.New("wp-config.php already defines DISABLE_WP_CRON outside WPX management")
		}
		anchor := []byte("/* That's all, stop editing!")
		index := strings.Index(string(content), string(anchor))
		if index < 0 {
			return errors.New("wp-config.php does not contain the WordPress configuration boundary")
		}
		updated := append([]byte{}, content[:index]...)
		updated = append(updated, []byte(wpCronMarker+"\n\n")...)
		updated = append(updated, content[index:]...)
		return atomicWriteOwned(path, updated, 0640, identity)
	}
	if match == nil {
		return nil
	}
	if strings.TrimSpace(string(match)) != wpCronMarker {
		return errors.New("refuse to remove an unmanaged DISABLE_WP_CRON definition")
	}
	updated := append([]byte{}, content[:wpCronDefine.FindIndex(content)[0]]...)
	rest := content[wpCronDefine.FindIndex(content)[1]:]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}
	updated = append(updated, rest...)
	return atomicWriteOwned(path, updated, 0640, identity)
}

func atomicWriteOwned(path string, content []byte, mode os.FileMode, identity Identity) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wpx-cron-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chown(identity.UID, identity.GID); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
