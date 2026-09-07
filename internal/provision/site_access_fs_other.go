//go:build !linux

package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func secureManagedAccessRead(root, name, marker string) ([]byte, bool, error) {
	if err := rejectAccessSymlinks(root); err != nil {
		return nil, false, err
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("managed file is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(content), marker) {
		return nil, false, errors.New("refuse to replace unmanaged file")
	}
	return content, true, nil
}

func secureAccessWrite(root, name string, content []byte, mode os.FileMode) error {
	if err := rejectAccessSymlinks(root); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(root, name), content, mode)
}

func secureAccessRemove(root, name string) error { return os.Remove(filepath.Join(root, name)) }
func secureAccessChown(root, name string, uid, gid int) error {
	return os.Chown(filepath.Join(root, name), uid, gid)
}

func rejectAccessSymlinks(root string) error {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(root, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed root contains a symlink or non-directory")
		}
	}
	return nil
}
