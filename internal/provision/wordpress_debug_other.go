//go:build !linux

package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func secureReadWordPressConfig(h *Host, site model.Site) ([]byte, error) {
	path := filepath.Join(h.SiteRoot, site.ID, "public", "wp-config.php")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("wp-config.php must be a regular file, not a symbolic link")
	}
	return os.ReadFile(path)
}

func secureSetWordPressDebug(ctx context.Context, h *Host, site model.Site, identity Identity, enabled bool, logPath string) error {
	path := filepath.Join(h.SiteRoot, site.ID, "public", "wp-config.php")
	content, err := secureReadWordPressConfig(h, site)
	if err != nil {
		return err
	}
	updated, changed, err := updateWordPressDebug(content, enabled, logPath)
	if err != nil || !changed {
		return err
	}
	info, _ := os.Stat(path)
	temporary := path + ".wpx-debug-lint"
	if err := atomicWrite(temporary, updated, info.Mode().Perm()); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := h.Runner.Run(ctx, "/usr/sbin/runuser", "--user", identity.Name, "--", "/usr/bin/php"+site.PHPVersion, "-l", "-f", temporary); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func securePrepareWordPressDebugLog(h *Host, site model.Site, identity Identity) (bool, error) {
	path := h.wordpressDebugLogPath(site)
	if err := ensureDirectory(filepath.Dir(path), 0700, identity); err != nil {
		return false, err
	}
	_, err := os.Lstat(path)
	created := os.IsNotExist(err)
	if err == nil {
		info, _ := os.Lstat(path)
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("private debug log must be a regular file")
		}
	} else if !created {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return false, err
	}
	file.Close()
	if err := os.Chmod(path, 0640); err != nil {
		return false, err
	}
	return created, os.Chown(path, identity.UID, identity.GID)
}

func secureWordPressDebugLogStatus(h *Host, site model.Site) (model.WordPressDebugStatus, error) {
	info, err := os.Lstat(h.wordpressDebugLogPath(site))
	if os.IsNotExist(err) {
		return model.WordPressDebugStatus{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return model.WordPressDebugStatus{}, errors.New("private debug log must be a regular file")
	}
	return model.WordPressDebugStatus{LogExists: true, LogSize: info.Size(), LogModifiedAt: info.ModTime().UTC().Format(time.RFC3339)}, nil
}

func secureReadWordPressDebugLog(h *Host, site model.Site, limit int64) (model.WordPressDebugLog, error) {
	path := h.wordpressDebugLogPath(site)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return model.WordPressDebugLog{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return model.WordPressDebugLog{}, errors.New("private debug log must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return model.WordPressDebugLog{}, err
	}
	defer file.Close()
	start := info.Size() - limit
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	_, _ = file.Seek(start, io.SeekStart)
	content, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return model.WordPressDebugLog{}, err
	}
	return model.WordPressDebugLog{Content: strings.ToValidUTF8(string(content), "�"), Size: info.Size(), Truncated: truncated}, nil
}

func secureClearWordPressDebugLog(h *Host, site model.Site) error {
	path := h.wordpressDebugLogPath(site)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("private debug log must be a regular file")
	}
	return os.Truncate(path, 0)
}

func secureRemoveWordPressDebugLog(h *Host, site model.Site) error {
	err := os.Remove(h.wordpressDebugLogPath(site))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
