package service

import (
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// TestMonitorStopDrains verifies Stop() returns after the run loop AND its
// fan-out probe goroutines have drained — the previous code closed doneCh as
// soon as the loop exited, leaving the per-machine/per-tunnel status writers
// untracked. A seeded machine/tunnel forces at least one probe per cycle; the
// probes dial an unbound loopback port and fail fast, so Stop should return
// well inside its budget.
func TestMonitorStopDrains(t *testing.T) {
	initTestDB(t)
	if err := db.CreateMachine(&db.Machine{
		ID: "mon-m1", Name: "m1", Username: "ubuntu",
		TunnelPort: 65533, Status: "connected",
	}); err != nil {
		t.Fatalf("seed machine: %v", err)
	}
	if err := db.CreateTunnel(&db.Tunnel{
		ID: "mon-t1", MachineID: "mon-m1", Name: "t1",
		LocalPort: 3000, RatholePort: 65532, Protocol: "tcp",
		Status: "active", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}

	m := NewMonitorService()
	m.Start()

	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("monitor Stop did not return — drain likely deadlocked")
	}

	// Idempotent second call must also return immediately.
	m.Stop()
}
