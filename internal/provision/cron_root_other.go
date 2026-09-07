//go:build !linux

package provision

import "os"

func openSecureCronRoot(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}
