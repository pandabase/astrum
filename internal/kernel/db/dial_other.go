//go:build !linux

package db

import "syscall"

var control func(network, address string, c syscall.RawConn) error
