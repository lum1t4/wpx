//go:build linux

package provision

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openSecureCronRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("cron root must be absolute, clean, and non-root")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, fmt.Errorf("cron root contains an unsafe component %q: %w", component, openErr)
		}
		fd = next
	}
	// OpenRoot pins the directory referenced by this already-validated descriptor.
	// The proc link is controlled by the kernel and cannot be redirected by a
	// concurrent rename of any original path component.
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	unix.Close(fd)
	return root, err
}
