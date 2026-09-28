package db

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDialerSetsTCPTimeouts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	conn, err := dialer(time.Second).DialContext(context.Background(), "tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][3]int{
		"TCP_USER_TIMEOUT": {unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(userTimeout.Milliseconds())},
		"SO_KEEPALIVE":     {unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1},
		"TCP_KEEPIDLE":     {unix.IPPROTO_TCP, unix.TCP_KEEPIDLE, 10},
		"TCP_KEEPINTVL":    {unix.IPPROTO_TCP, unix.TCP_KEEPINTVL, 5},
		"TCP_KEEPCNT":      {unix.IPPROTO_TCP, unix.TCP_KEEPCNT, 3},
	}
	for name, opt := range want {
		var got int
		var gerr error
		if err := raw.Control(func(fd uintptr) { got, gerr = unix.GetsockoptInt(int(fd), opt[0], opt[1]) }); err != nil || gerr != nil {
			t.Fatalf("%s: %v %v", name, err, gerr)
		}

		if got != opt[2] {
			t.Errorf("%s = %d, want %d", name, got, opt[2])
		}
	}
}

func TestControlSkipsUnixSockets(t *testing.T) {
	path := t.TempDir() + "/s"
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	conn, err := dialer(time.Second).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

}
