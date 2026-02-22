package main

import (
	"net"
	"testing"
)

func TestSelectAvailablePortSkipsBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port
	got, err := selectAvailablePort(busyPort, 2)
	if err != nil {
		t.Fatalf("selectAvailablePort returned err: %v", err)
	}
	if got == busyPort {
		t.Fatalf("expected non-busy port, got busy port %d", got)
	}
}
