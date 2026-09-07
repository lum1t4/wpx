//go:build linux

package provision

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxSecurityConfigBytes = 1 << 20

// openSecurityDirectory walks from the filesystem root with O_NOFOLLOW on
// every component. Holding the returned descriptor pins the directory while a
// managed file is inspected, replaced, or removed.
func openSecurityDirectory(path string, create bool) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, errors.New("security directory must be absolute, clean, and non-root")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) && create {
			if mkdirErr := unix.Mkdirat(fd, component, 0755); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				unix.Close(fd)
				return -1, mkdirErr
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, errors.New("security configuration path contains an unsafe directory")
		}
		fd = next
	}
	return fd, nil
}

func ensureSecurityDirectory(path string) error {
	fd, err := openSecurityDirectory(path, true)
	if err == nil {
		err = unix.Close(fd)
	}
	return err
}

func securityManagedState(path string) ([]byte, bool, error) {
	fd, err := openSecurityDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, false, err
	}
	defer unix.Close(fd)
	fileFD, err := unix.Openat(fd, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.New("security configuration is not a regular managed file")
	}
	file := os.NewFile(uintptr(fileFD), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("security configuration is not a regular managed file")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSecurityConfigBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > maxSecurityConfigBytes {
		return nil, false, errors.New("security configuration is too large")
	}
	if !strings.HasPrefix(string(content), securityMarker) {
		return nil, false, errors.New("refuse to replace unmanaged file")
	}
	return content, true, nil
}

func secureAtomicWrite(path string, content []byte, mode os.FileMode) error {
	fd, err := openSecurityDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := ".wpx-security-" + hex.EncodeToString(random)
	tmpFD, err := unix.Openat(fd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(tmpFD), temporary)
	cleanup := func() { file.Close(); _ = unix.Unlinkat(fd, temporary, 0) }
	if _, err := file.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = unix.Unlinkat(fd, temporary, 0)
		return err
	}
	if err := unix.Renameat(fd, temporary, fd, filepath.Base(path)); err != nil {
		_ = unix.Unlinkat(fd, temporary, 0)
		return err
	}
	return nil
}

func secureRemove(path string) error {
	fd, err := openSecurityDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Unlinkat(fd, filepath.Base(path), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
