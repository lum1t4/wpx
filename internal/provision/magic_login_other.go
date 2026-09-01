//go:build !linux

package provision

import (
	"context"
	"errors"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

func (h *Host) MagicLogin(context.Context, model.Site) (string, time.Time, error) {
	return "", time.Time{}, errors.New("WordPress magic login requires Linux")
}
