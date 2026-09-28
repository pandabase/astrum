package main

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestHardenDisablesDumping(t *testing.T) {
	if err := harden(); err != nil {
		t.Fatal(err)
	}

	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || dumpable != 0 {
		t.Fatalf("dumpable = %d, %v", dumpable, err)
	}
}
