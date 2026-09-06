package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/api/dto"
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

func (f *fakeLocalOps) WriteServiceTunnelCaddy(tunnel *db.Tunnel) error {
	f.calls = append(f.calls, "caddy-write:"+tunnel.ID)
	return nil
}

func (f *fakeLocalOps) hasCall(want string) bool {
	for _, c := range f.calls {
		if c == want {
			return true
		}
	}
	return false
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

func seedDomain(t *testing.T, domain string) {
	t.Helper()
	if err := db.SaveSettings(&db.AppSettings{Domain: domain}); err != nil {
		t.Fatalf("failed to seed settings: %v", err)
	}
}

// Regression for the subdomain-edit Caddy bug: editing a tunnel's subdomain
// updated the DB but left Caddy serving the old subdomain, because oldSubdomain
// was captured *after* the value had already been mutated (so the
// "did the subdomain change?" compare was always false and the Caddy rewrite
// never fired). This locks in that a subdomain change rewrites the Caddy block.
func TestTunnelUpdate_SubdomainChangeRewritesCaddy(t *testing.T) {
	initTestDB(t)
	seedDomain(t, "example.com")
	seedMachine(t, "m1", 10031)
	seedTunnel(t, "t1", "m1", 3000, 20031)
	tun, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	tun.Subdomain = "old"
	tun.Transport = "tcp"
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	if _, err := svc.Update("t1", dto.UpdateTunnelRequest{Name: "t1", Subdomain: "new", LocalPort: 3000}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subdomain != "new" {
		t.Fatalf("expected subdomain persisted as 'new', got %q", got.Subdomain)
	}
	if !fake.hasCall("caddy-write:t1") {
		t.Fatalf("subdomain change must rewrite the Caddy block; got calls %v", fake.calls)
	}
}

// A name-only edit shouldn't touch Caddy (name isn't part of the Caddy block).
func TestTunnelUpdate_NameOnlyChangeLeavesCaddyAlone(t *testing.T) {
	initTestDB(t)
	seedDomain(t, "example.com")
	seedMachine(t, "m1", 10041)
	seedTunnel(t, "t1", "m1", 3000, 20041)
	tun, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	tun.Subdomain = "keep"
	tun.Transport = "tcp"
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	if _, err := svc.Update("t1", dto.UpdateTunnelRequest{Name: "renamed", Subdomain: "keep", LocalPort: 3000}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" {
		t.Fatalf("expected name persisted as 'renamed', got %q", got.Name)
	}
	if fake.hasCall("caddy-write:t1") {
		t.Fatalf("name-only edit must not touch Caddy; got calls %v", fake.calls)
	}
}

// Regression for "private mode removes URL hint": toggling a tunnel to private
// must KEEP its subdomain (private = reverse-proxy-only, not fully hidden) and
// rewrite the Caddy block (the upstream depends on privacy). The old code wiped
// the subdomain on private, so the dashboard URL hint vanished.
func TestTunnelUpdate_ToggleToPrivateKeepsSubdomainAndRewritesCaddy(t *testing.T) {
	initTestDB(t)
	seedDomain(t, "example.com")
	seedMachine(t, "m1", 10051)
	seedTunnel(t, "t1", "m1", 3000, 20051)
	tun, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	tun.Subdomain = "api"
	tun.Transport = "tcp"
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	if _, err := svc.Update("t1", dto.UpdateTunnelRequest{Name: "t1", Subdomain: "api", LocalPort: 3000, Private: true}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Subdomain != "api" {
		t.Fatalf("toggle-to-private must KEEP the subdomain; got %q", got.Subdomain)
	}
	if !got.Private {
		t.Fatalf("expected tunnel to be private")
	}
	if !fake.hasCall("caddy-write:t1") {
		t.Fatalf("toggle-to-private must rewrite the Caddy block (upstream depends on privacy); got %v", fake.calls)
	}
}

// Bot protection is bypassable unless the raw port is closed, so enabling it
// must force the tunnel private even when the request asks for public.
func TestTunnelUpdate_BotProtectionForcesPrivate(t *testing.T) {
	initTestDB(t)
	seedDomain(t, "example.com")
	seedMachine(t, "m1", 10061)
	seedTunnel(t, "t1", "m1", 3000, 20061)
	tun, err := db.GetTunnel("t1")
	if err != nil {
		t.Fatal(err)
	}
	tun.Subdomain = "api"
	tun.Transport = "tcp"
	if err := db.UpdateTunnel(tun); err != nil {
		t.Fatal(err)
	}

	fake := &fakeLocalOps{}
	svc := NewTunnelService(fake)
	got, err := svc.Update("t1", dto.UpdateTunnelRequest{Name: "t1", Subdomain: "api", LocalPort: 3000, Private: false, BotProtectionEnabled: true})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !got.BotProtectionEnabled {
		t.Fatalf("expected bot protection enabled")
	}
	if !got.Private {
		t.Fatalf("bot protection must coerce a public tunnel to private; got Private=false")
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
