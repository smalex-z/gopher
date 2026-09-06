package service

import (
	"net"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

// TestProbeUDPPath verifies the UDP round-trip: a datagram that gets echoed back
// through the tunnel proves the path ("active"); a bound-but-silent port yields
// the ambiguous "silent" that the caller then disambiguates via the agent.
func TestProbeUDPPath(t *testing.T) {
	// Echo server stands in for "origin service reachable through the tunnel".
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 32)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()
	echoPort := echo.LocalAddr().(*net.UDPAddr).Port
	if got := probeUDPPath(db.Tunnel{Transport: "udp", Private: true, RatholePort: echoPort}); got != "active" {
		t.Errorf("echo udp port: expected active, got %q", got)
	}

	// A bound socket that never replies → ambiguous "silent".
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	silentPort := silent.LocalAddr().(*net.UDPAddr).Port
	if got := probeUDPPath(db.Tunnel{Transport: "udp", Private: true, RatholePort: silentPort}); got != "silent" {
		t.Errorf("silent udp port: expected silent, got %q", got)
	}
}

// TestDisambiguateFallback covers the no-agent fallback of the ambiguous case:
// an offline machine → "offline", a reachable machine → "connected". (The agent
// path needs a live agent and is exercised end-to-end, not here.)
func TestDisambiguateFallback(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "up", 5200) // seeded "connected", no agent

	if got := disambiguate(db.Tunnel{MachineID: "up", Transport: "tcp"}); got != "connected" {
		t.Errorf("reachable non-agent machine: expected connected, got %q", got)
	}

	if err := db.SetMachineStatus("up", "offline", nil); err != nil {
		t.Fatal(err)
	}
	if got := disambiguate(db.Tunnel{MachineID: "up", Transport: "tcp"}); got != "offline" {
		t.Errorf("offline machine: expected offline, got %q", got)
	}

	// Orphan tunnel (no machine, no agent) → treated as up (can't prove down).
	if got := disambiguate(db.Tunnel{Transport: "udp"}); got != "connected" {
		t.Errorf("orphan tunnel: expected connected, got %q", got)
	}
}

// TestAgentPortListeningFallback: a machine without an installed agent has no
// CheckPorts source, so agentPortListening must decline (ok=false).
func TestAgentPortListeningFallback(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1", 5200) // AgentInstalled=false, AgentRemotePort=0

	if _, ok := agentPortListening(db.Tunnel{MachineID: "m1", Transport: "tcp", LocalPort: 3000}); ok {
		t.Error("non-agent machine: expected agentPortListening to decline (ok=false)")
	}
	if _, ok := agentPortListening(db.Tunnel{Transport: "udp"}); ok {
		t.Error("orphan tunnel: expected agentPortListening to decline")
	}
}
