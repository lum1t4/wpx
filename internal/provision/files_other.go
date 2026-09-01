//go:build !linux

package provision

import (
	"context"
	"errors"

	"github.com/lum1t4/wpx/internal/broker"
	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) ListFiles(context.Context, model.Site, string) ([]broker.FileEntry, error) {
	return nil, errors.New("file operations require Linux")
}

func (h *Host) ReadFile(context.Context, model.Site, string) (string, error) {
	return "", errors.New("file operations require Linux")
}

func (h *Host) WriteFile(context.Context, model.Site, string, string) error {
	return errors.New("file operations require Linux")
}
