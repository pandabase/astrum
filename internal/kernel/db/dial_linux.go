package db

import (
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func control(network, _ string, c syscall.RawConn) error {
	if !strings.HasPrefix(network, "tcp") {
		return nil
	}

	var err error
	if cerr := c.Control(func(fd uintptr) {
		err = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(userTimeout.Milliseconds()))
	}); cerr != nil {
		return cerr
	}

	return err
}
