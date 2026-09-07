//go:build linux

package provision

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func secureAccessDir(root string, create bool) (int, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return -1, errors.New("managed root must be absolute, clean, and non-root")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
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
			return -1, fmt.Errorf("open managed directory component %q: %w", component, openErr)
		}
		fd = next
	}
	return fd, nil
}

func secureManagedAccessRead(root, name, marker string) ([]byte, bool, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return nil, false, errors.New("invalid managed filename")
	}
	dirfd, err := secureAccessDir(root, false)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("managed file is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > 2<<20 {
		return nil, false, errors.New("managed file is too large")
	}
	if !strings.HasPrefix(string(content), marker) {
		return nil, false, errors.New("refuse to replace unmanaged file")
	}
	return content, true, nil
}

func secureAccessWrite(root, name string, content []byte, mode os.FileMode) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return errors.New("invalid managed filename")
	}
	dirfd, err := secureAccessDir(root, true)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	var existing unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &existing, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if existing.Mode&unix.S_IFMT != unix.S_IFREG {
			return errors.New("refuse to replace non-regular managed file")
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := ".wpx-" + hex.EncodeToString(random)
	fd, err := unix.Openat(dirfd, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temporary)
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = unix.Unlinkat(dirfd, temporary, 0)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(dirfd, temporary, dirfd, name); err != nil {
		return err
	}
	ok = true
	return nil
}

func secureAccessRemove(root, name string) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return errors.New("invalid managed filename")
	}
	dirfd, err := secureAccessDir(root, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	if err := unix.Unlinkat(dirfd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func secureAccessChown(root, name string, uid, gid int) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return errors.New("invalid managed filename")
	}
	dirfd, err := secureAccessDir(root, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fchown(fd, uid, gid)
}
