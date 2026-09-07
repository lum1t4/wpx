//go:build !linux

package provision

import "github.com/lum1t4/wpx/internal/model"

func secureSetWPInternalCron(_ *Host, _ model.Site, path string, disable bool, identity Identity) error {
	return setWPInternalCron(path, disable, identity)
}
