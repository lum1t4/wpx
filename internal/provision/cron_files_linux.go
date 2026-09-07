//go:build linux

package provision

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lum1t4/wpx/internal/model"
	"golang.org/x/sys/unix"
)

// secureSetWPInternalCron pins the public directory before reading or writing.
// Site users can rename entries in their tree while the broker runs as root;
// path-based Lstat followed by CreateTemp would otherwise permit an ancestor
// symlink swap between those operations.
func secureSetWPInternalCron(h *Host, site model.Site, _ string, disable bool, identity Identity) error {
	rootFD, _, err := h.openPublicRoot(site)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	const name = "wp-config.php"
	fd, err := unix.Openat2(rootFD, name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return fmt.Errorf("open wp-config.php safely: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return errors.New("create wp-config.php handle")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return errors.New("wp-config.php must be a regular file, not a symbolic link")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxEditableFile+1))
	file.Close()
	if err != nil {
		return fmt.Errorf("read wp-config.php: %w", err)
	}
	if len(content) > maxEditableFile {
		return errors.New("wp-config.php exceeds the 1 MiB managed file limit")
	}
	updated, changed, err := updateWPInternalCron(content, disable)
	if err != nil || !changed {
		return err
	}
	temporary, err := randomTemporaryName()
	if err != nil {
		return err
	}
	if err := createFileAt(rootFD, temporary, updated, info.Mode().Perm(), identity); err != nil {
		return err
	}
	defer unix.Unlinkat(rootFD, temporary, 0)
	if err := unix.Renameat(rootFD, temporary, rootFD, name); err != nil {
		return fmt.Errorf("replace wp-config.php safely: %w", err)
	}
	return nil
}

func updateWPInternalCron(content []byte, disable bool) ([]byte, bool, error) {
	match := wpCronDefine.Find(content)
	if disable {
		if match != nil {
			if strings.TrimSpace(string(match)) == wpCronMarker {
				return content, false, nil
			}
			return nil, false, errors.New("wp-config.php already defines DISABLE_WP_CRON outside WPX management")
		}
		anchor := []byte("/* That's all, stop editing!")
		index := strings.Index(string(content), string(anchor))
		if index < 0 {
			return nil, false, errors.New("wp-config.php does not contain the WordPress configuration boundary")
		}
		updated := append([]byte{}, content[:index]...)
		updated = append(updated, []byte(wpCronMarker+"\n\n")...)
		updated = append(updated, content[index:]...)
		return updated, true, nil
	}
	if match == nil {
		return content, false, nil
	}
	if strings.TrimSpace(string(match)) != wpCronMarker {
		return nil, false, errors.New("refuse to remove an unmanaged DISABLE_WP_CRON definition")
	}
	indices := wpCronDefine.FindIndex(content)
	updated := append([]byte{}, content[:indices[0]]...)
	rest := content[indices[1]:]
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}
	updated = append(updated, rest...)
	return updated, true, nil
}
