package db

import (
	"net"
	"testing"
)

// TestPortAvailable checks the live OS probe that gates explicit rathole ports:
// a port held by a running listener reads as unavailable, a free one as
// available. This is what lets the tunnel-create path reject a port occupied by
// a core listener (rathole's 2333, Caddy, the dashboard, sshd) without gopher
// hardcoding which ports those are.
func TestPortAvailable(t *testing.T) {
	// Occupy an OS-assigned port and confirm the probe sees it as taken.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	occupied := l.Addr().(*net.TCPAddr).Port
	if PortAvailable(occupied) {
		t.Errorf("port %d is held by a listener but PortAvailable returned true", occupied)
	}

	// After closing it, the same port should read as free again.
	_ = l.Close()
	if !PortAvailable(occupied) {
		t.Errorf("port %d was released but PortAvailable returned false", occupied)
	}
}
