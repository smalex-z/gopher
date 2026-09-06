package db

import (
	"testing"
	"time"
)

// TestOnStatusChange_FiresOnlyOnTransition: the status hook must fire when a
// machine/tunnel status actually changes and stay silent on same-status
// rewrites — otherwise the dashboard WS gets an event storm every 30s poll
// cycle even when nothing changed.
func TestOnStatusChange_FiresOnlyOnTransition(t *testing.T) {
	initTestDB(t)

	type event struct{ kind, id, status string }
	var events []event
	orig := OnStatusChange
	OnStatusChange = func(kind, id, status string) {
		events = append(events, event{kind, id, status})
	}
	t.Cleanup(func() { OnStatusChange = orig })

	seedMachine(t, "m1") // Status "active" → create fires one machine event
	if len(events) != 1 || events[0] != (event{"machine", "m1", "active"}) {
		t.Fatalf("expected create event for m1, got %+v", events)
	}
	events = nil

	now := time.Now()
	if err := SetMachineStatus("m1", "connected", &now); err != nil {
		t.Fatalf("SetMachineStatus: %v", err)
	}
	if len(events) != 1 || events[0] != (event{"machine", "m1", "connected"}) {
		t.Fatalf("expected transition event active→connected, got %+v", events)
	}

	// Same status again — the periodic monitor rewrite must NOT re-fire.
	if err := SetMachineStatus("m1", "connected", &now); err != nil {
		t.Fatalf("SetMachineStatus: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("same-status rewrite fired a duplicate event: %+v", events)
	}
	events = nil

	tun := &Tunnel{ID: "t1", MachineID: "m1", Name: "t1", Status: "offline", RatholePort: 9001}
	if err := CreateTunnel(tun); err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	events = nil

	if err := SetTunnelStatus("t1", "active"); err != nil {
		t.Fatalf("SetTunnelStatus: %v", err)
	}
	if err := SetTunnelStatus("t1", "active"); err != nil {
		t.Fatalf("SetTunnelStatus: %v", err)
	}
	if len(events) != 1 || events[0] != (event{"tunnel", "t1", "active"}) {
		t.Fatalf("expected exactly one tunnel transition event, got %+v", events)
	}
	events = nil

	if err := DeleteTunnel("t1"); err != nil {
		t.Fatalf("DeleteTunnel: %v", err)
	}
	if len(events) != 1 || events[0] != (event{"tunnel", "t1", "deleted"}) {
		t.Fatalf("expected tunnel delete event, got %+v", events)
	}
}
