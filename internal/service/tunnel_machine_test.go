package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	apperrors "github.com/smalex-z/gopher/internal/errors"
)

type fakeLocalOps struct {
	calls []string
}

func (f *fakeLocalOps) AddServiceTunnel(_ *db.Tunnel, _ *db.Machine) error { return nil }

func (f *fakeLocalOps) RemoveServiceTunnelClient(tunnel *db.Tunnel, _ *db.Machine) error {
	f.calls = append(f.calls, "client:"+tunnel.ID)
	return nil
}

func (f *fakeLocalOps) ReconcileServerConfig() error {
	f.calls = append(f.calls, "server:reconcile")
	return nil
}

func (f *fakeLocalOps) RemoveServiceTunnelCaddy(tunnel *db.Tunnel) error {
	f.calls = append(f.calls, "caddy:"+tunnel.ID)
	return nil
}

func (f *fakeLocalOps) RemoveMachineClient(machine *db.Machine) error {
	f.calls = append(f.calls, "machine:remove-client:"+machine.ID)
	return nil
}

func initTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	if err := db.Initialize(dsn); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
}

func seedMachine(t *testing.T, id string, tunnelPort int) *db.Machine {
	t.Helper()
	m := &db.Machine{
		ID:              id,
		Name:            "machine-" + id,
		Username:        "ubuntu",
		TunnelPort:      tunnelPort,
		RatholeSSHToken: "ssh-token-" + id,
		Status:          "connected",
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := db.CreateMachine(m); err != nil {
		t.Fatalf("failed to seed machine: %v", err)
	}
	return m
}

func seedTunnel(t *testing.T, id, machineID string, localPort, ratholePort int) *db.Tunnel {
	t.Helper()
	tun := &db.Tunnel{
		ID:           id,
		MachineID:    machineID,
		Name:         "tunnel-" + id,
		Subdomain:    "",
		LocalPort:    localPort,
		RatholePort:  ratholePort,
		RatholeToken: "token-" + id,
		Protocol:     "tcp",
		Status:       "active",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.CreateTunnel(tun); err != nil {
		t.Fatalf("failed to seed tunnel: %v", err)
	}
	return tun
}

func TestTunnelDelete_OrderClientServerCaddy(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1", 10001)
	seedTunnel(t, "t1", "m1", 3000, 20001)

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	if err := svc.Delete("t1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if _, err := db.GetTunnel("t1"); err == nil {
		t.Fatal("expected tunnel to be deleted from DB")
	}

	want := []string{"client:t1", "server:reconcile", "caddy:t1"}
	if len(fake.calls) != len(want) {
		t.Fatalf("unexpected calls: got %v want %v", fake.calls, want)
	}
	for i := range want {
		if fake.calls[i] != want[i] {
			t.Fatalf("unexpected call order at %d: got %q want %q (all=%v)", i, fake.calls[i], want[i], fake.calls)
		}
	}
}

func TestTunnelDelete_RejectsMachineSSHTunnelID(t *testing.T) {
	initTestDB(t)
	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)

	err := svc.Delete("machine-m1-ssh")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*apperrors.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T (%v)", err, err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected no side-effect calls, got %v", fake.calls)
	}
}

func TestTunnelDelete_RejectsPort22Tunnel(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1", 10001)
	seedTunnel(t, "ssh-like", "m1", 22, 20022)

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	err := svc.Delete("ssh-like")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if _, ok := err.(*apperrors.ValidationError); !ok {
		t.Fatalf("expected ValidationError, got %T (%v)", err, err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("expected no side effects, got %v", fake.calls)
	}
	if _, err := db.GetTunnel("ssh-like"); err != nil {
		t.Fatalf("port-22 tunnel should remain in DB, got err=%v", err)
	}
}

func TestTunnelList_IncludesMachineSSHTunnel(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1", 10011)
	seedTunnel(t, "app", "m1", 3000, 20011)

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	tunnels, err := svc.List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}

	foundMachineTunnel := false
	for _, tunnel := range tunnels {
		if tunnel.ID == "machine-m1-ssh" {
			foundMachineTunnel = true
			if tunnel.LocalPort != 22 {
				t.Fatalf("expected machine tunnel local port 22, got %d", tunnel.LocalPort)
			}
			if !tunnel.Managed || tunnel.Kind != "machine-ssh" {
				t.Fatalf("expected managed machine ssh tunnel flags, got managed=%v kind=%q", tunnel.Managed, tunnel.Kind)
			}
		}
	}
	if !foundMachineTunnel {
		t.Fatalf("expected synthesized machine SSH tunnel in list, got: %+v", tunnels)
	}
}

func TestMachineDelete_DeletesTunnelsThenMachineClientThenMachine(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1", 10021)
	seedTunnel(t, "t1", "m1", 3001, 20021)
	seedTunnel(t, "t2", "m1", 3002, 20022)

	fake := &fakeLocalOps{}
	svc := NewMachineService(nil, fake)
	res, err := svc.Delete("m1")
	if err != nil {
		t.Fatalf("machine delete failed: %v", err)
	}
	if res == nil || res.ID != "m1" {
		t.Fatalf("expected DeleteResult for m1, got %+v", res)
	}
	if !res.ClientCleanupOK {
		t.Fatalf("expected client cleanup ok with fake local ops, got %+v", res)
	}

	if _, err := db.GetMachine("m1"); err == nil {
		t.Fatal("expected machine to be deleted")
	}
	if _, err := db.GetTunnel("t1"); err == nil {
		t.Fatal("expected t1 to be deleted")
	}
	if _, err := db.GetTunnel("t2"); err == nil {
		t.Fatal("expected t2 to be deleted")
	}

	// RemoveMachineClient must be called first — before any rathole reconciles —
	// so the SSH back-channel is still up when we trigger the uninstall script.
	if len(fake.calls) == 0 || fake.calls[0] != "machine:remove-client:m1" {
		t.Fatalf("expected machine:remove-client:m1 as first call, got %v", fake.calls)
	}

	// Per-tunnel RemoveServiceTunnelClient is NOT called during machine delete;
	// gopher-uninstall handles full cleanup. Only caddy entries and a single
	// server reconcile should follow.
	for _, call := range fake.calls[1:] {
		if strings.HasPrefix(call, "client:") {
			t.Fatalf("unexpected per-tunnel client cleanup call during machine delete: %v", fake.calls)
		}
	}
}
