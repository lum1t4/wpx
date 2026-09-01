//go:build linux

package broker

import (
	"errors"
	"net"
	"syscall"
)

func peerUID(conn net.Conn) (uint32, error) {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("broker connection is not Unix")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var inner error
	err = raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			inner = err
			return
		}
		uid = cred.Uid
	})
	if err != nil {
		return 0, err
	}
	return uid, inner
}
