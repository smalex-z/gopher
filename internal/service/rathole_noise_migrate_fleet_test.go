package service

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
)

// Regression for the upgrade bug that took every tunnel on an install down.
//
// MigrateRatholeNoise used to be called during the startup reconcile, ~200ms
// BEFORE the supervisor started rathole. Its step-2 "push a noise-ready
// client.toml to every machine over the still-working plaintext tunnel" then
// failed for every machine in milliseconds (there was no rathole process, so
// no tunnel), it logged them as "offline", and step 3 flipped the server to
// noise anyway — leaving the entire fleet holding plaintext configs that can
// no longer complete a handshake. Every machine went offline on every upgrade.
//
// The fix has two halves. The call site moved to after startBundledChildren so
// a tunnel actually exists, and the migration itself became all-or-nothing:
// unreachable machines abort it BEFORE any key is minted or any config
// touched. These tests pin the second half — the half that makes the outcome
// safe even if the ordering ever regresses again.
func withShortFleetWait(t *testing.T) {
	t.Helper()
	oldWait, oldInterval, oldProbe, oldSettle := noiseMigrationFleetWait, noiseMigrationProbeInterval, noiseMigrationProbeTimeout, noiseMigrationSettleWait
	noiseMigrationFleetWait = 50 * time.Millisecond
	noiseMigrationProbeInterval = 10 * time.Millisecond
	noiseMigrationProbeTimeout = 200 * time.Millisecond
	noiseMigrationSettleWait = 50 * time.Millisecond
	t.Cleanup(func() {
		noiseMigrationFleetWait, noiseMigrationProbeInterval, noiseMigrationProbeTimeout, noiseMigrationSettleWait = oldWait, oldInterval, oldProbe, oldSettle
	})
}

func markSetup(t *testing.T) {
	t.Helper()
	if err := db.MutateSettings(func(s *db.AppSettings) error {
		s.IsSetup = true
		s.Domain = "example.com"
		return nil
	}); err != nil {
		t.Fatalf("mark setup: %v", err)
	}
}

// useTempRatholeConfig points both the reconcile path and the custom-service
// detector at a temp file so the test can assert on what was (not) written.
func useTempRatholeConfig(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir() + "/server.toml"
	origPaths, origDetect := paths.RatholeConfig, ratholeServerTomlPath
	paths.RatholeConfig = cfg
	ratholeServerTomlPath = cfg
	t.Cleanup(func() {
		paths.RatholeConfig = origPaths
		ratholeServerTomlPath = origDetect
	})
	return cfg
}

// An unreachable machine must abort the migration outright: no keypair, no
// server flip, nothing written. The install has to be left exactly as it was —
// on plaintext, with every tunnel still working — because flipping the server
// is what strands that machine.
func TestMigrateRatholeNoise_UnreachableMachineAbortsBeforeAnyChange(t *testing.T) {
	initTestDB(t)
	withShortFleetWait(t)
	cfg := useTempRatholeConfig(t)
	markSetup(t)

	// Agent port 1 is closed, so the reachability probe fails fast — the same
	// observable state as a machine still mid-reconnect after a restart.
	if err := db.CreateMachine(&db.Machine{
		ID: "m1", Name: "temp-screen", Username: "ubuntu",
		TunnelPort: 20001, AgentRemotePort: 1, AgentInstalled: true,
		Status: "connected", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create machine: %v", err)
	}

	svc := &LocalSetupService{}
	if err := svc.MigrateRatholeNoise(); err != nil {
		t.Fatalf("a deferred migration is a normal outcome, not an error: %v", err)
	}

	settings, err := db.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings.RatholeNoisePrivKey != "" || settings.RatholeNoisePubKey != "" {
		t.Fatal("noise keypair was minted despite an unreachable machine — any later reconcile " +
			"would now emit [server.transport] and strand the fleet on plaintext")
	}
	if _, statErr := os.Stat(cfg); statErr == nil {
		t.Fatal("server.toml was written during a deferred migration — the server must not flip")
	}

	var blocked []string
	if settings.RatholeNoiseBlockedMachines != "" {
		if err := json.Unmarshal([]byte(settings.RatholeNoiseBlockedMachines), &blocked); err != nil {
			t.Fatalf("decode blocked machines: %v", err)
		}
	}
	if len(blocked) != 1 || blocked[0] != "temp-screen" {
		t.Fatalf("the blocking machine must be recorded for the dashboard, got %v", blocked)
	}
}

// A machine that never finished bootstrap (no tunnel port, no agent port) has
// no client.toml deployed anywhere, so it cannot be stranded and must not hold
// the migration hostage forever.
func TestMigrateRatholeNoise_UnbootstrappedMachineDoesNotBlock(t *testing.T) {
	initTestDB(t)
	withShortFleetWait(t)
	markSetup(t)

	if err := db.CreateMachine(&db.Machine{
		ID: "m2", Name: "never-bootstrapped", Username: "ubuntu",
		Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create machine: %v", err)
	}

	machines, err := db.GetMachines()
	if err != nil {
		t.Fatalf("get machines: %v", err)
	}
	if got := migratableMachines(machines); len(got) != 0 {
		t.Fatalf("unbootstrapped machine must be excluded from the fleet, got %d", len(got))
	}
}

// The abort path must drop the keypair. ReconcileServerConfig emits the noise
// transport block whenever the private key is set, so a key left behind after
// an aborted migration turns the next unrelated reconcile — a tunnel create,
// say — into the very fleet-wide flip the abort was avoiding.
func TestClearRatholeNoiseKeys_RemovesKeypair(t *testing.T) {
	initTestDB(t)
	if _, _, err := EnsureRatholeNoiseKeys(); err != nil {
		t.Fatalf("mint keys: %v", err)
	}
	if s, _ := db.GetSettings(); s.RatholeNoisePrivKey == "" {
		t.Fatal("precondition: keys should exist")
	}
	clearRatholeNoiseKeys()
	s, err := db.GetSettings()
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if s.RatholeNoisePrivKey != "" || s.RatholeNoisePubKey != "" {
		t.Fatal("keypair survived the abort path — the next reconcile would flip the server to noise")
	}
}
