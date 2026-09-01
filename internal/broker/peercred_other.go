//go:build !linux

package broker

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("broker peer credentials require Linux")
}
