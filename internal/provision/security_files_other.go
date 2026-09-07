//go:build !linux

package provision

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func ensureSecurityDirectory(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("security configuration directory is not a real directory")
	}
	return nil
}

func securityManagedState(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("security configuration is not a regular managed file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if !strings.HasPrefix(string(content), securityMarker) {
		return nil, false, errors.New("refuse to replace unmanaged file")
	}
	return content, true, nil
}

func secureAtomicWrite(path string, content []byte, mode os.FileMode) error {
	return atomicWrite(path, content, mode)
}
func secureRemove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
