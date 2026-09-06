package service

import (
	"errors"
	"net"
	"testing"

	"github.com/smalex-z/gopher/internal/api/dto"
	apperrors "github.com/smalex-z/gopher/internal/errors"
)

// TestCreateTunnelRejectsInUsePort verifies the explicit-rathole-port path
// rejects a port that's occupied by a live process on the edge — the hole that
// previously let a user pin a tunnel onto e.g. 2333 (rathole's own control
// channel) and take the whole tunnel server down. Enforcement is a real OS
// bind probe, so any occupied port (not a hardcoded list) is caught.
func TestCreateTunnelRejectsInUsePort(t *testing.T) {
	initTestDB(t)
	svc := NewTunnelService(&fakeLocalOps{})

	// Stand up a real listener to occupy a port, mimicking rathole/Caddy/sshd.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	occupied := l.Addr().(*net.TCPAddr).Port

	_, err = svc.Create(dto.CreateTunnelRequest{
		MachineID:   "m1",
		Name:        "collides",
		LocalPort:   3000,
		RatholePort: occupied,
	})
	if err == nil {
		t.Fatal("expected Create to reject an in-use rathole port, got nil")
	}
	var conflict *apperrors.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}
}

// TestCheckServerPort covers the pre-submit availability endpoint's logic: a
// live-occupied port and a privileged port both report unavailable with a
// reason; a free non-privileged port reports available.
func TestCheckServerPort(t *testing.T) {
	initTestDB(t)
	svc := NewTunnelService(&fakeLocalOps{})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	occupied := l.Addr().(*net.TCPAddr).Port

	if ok, reason := svc.CheckServerPort(occupied); ok || reason == "" {
		t.Errorf("occupied port: expected unavailable with reason, got ok=%v reason=%q", ok, reason)
	}
	if ok, reason := svc.CheckServerPort(443); ok || reason == "" {
		t.Errorf("privileged port: expected unavailable with reason, got ok=%v reason=%q", ok, reason)
	}
	// 0 = nothing typed yet → treated as available (no premature warning).
	if ok, _ := svc.CheckServerPort(0); !ok {
		t.Error("port 0 (unset) should report available")
	}
}

// TestCreateTunnelRejectsPrivilegedPort confirms the edge listener port can't be
// privileged (<1024) — enforced by config.ValidatePort on the explicit path.
func TestCreateTunnelRejectsPrivilegedPort(t *testing.T) {
	initTestDB(t)
	svc := NewTunnelService(&fakeLocalOps{})

	_, err := svc.Create(dto.CreateTunnelRequest{
		MachineID:   "m1",
		Name:        "privileged",
		LocalPort:   3000,
		RatholePort: 443, // privileged — must be rejected before any OS probe
	})
	if err == nil {
		t.Fatal("expected Create to reject a privileged rathole port, got nil")
	}
	var verr *apperrors.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
}
