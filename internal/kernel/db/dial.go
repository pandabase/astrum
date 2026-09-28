package db

import (
	"net"
	"time"
)

const userTimeout = 30 * time.Second

func dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     10 * time.Second,
			Interval: 5 * time.Second,
			Count:    3,
		},
		Control: control,
	}
}
