package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureManagedModeAtStartup is the state-based counterpart to Apply()'s
// patch — see managed_mode.go for why Apply() alone can't cover every path
// that reaches a fixed version. These tests exercise the marker/retry
// bookkeeping without touching a real systemd unit.

func withSudoStub(t *testing.T, dir string) {
	t.Helper()
	stub := filepath.Join(dir, "sudo")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nif [ \"$2\" = tee ]; then exec tee \"$3\"; fi\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write sudo stub: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestEnsureManagedModeAtStartup_NoOpWhenAlreadyManaged(t *testing.T) {
	t.Setenv("GOPHER_MANAGED", "1")
	dir := t.TempDir()
	origMarker := managedModeHealMarker
	managedModeHealMarker = filepath.Join(dir, "marker")
	t.Cleanup(func() { managedModeHealMarker = origMarker })

	EnsureManagedModeAtStartup()

	if _, err := os.Stat(managedModeHealMarker); err == nil {
		t.Error("must not attempt anything (or write a marker) when already managed")
	}
}

func TestEnsureManagedModeAtStartup_PatchesUnitAndWritesMarkerOnce(t *testing.T) {
	dir := t.TempDir()

	unitPath := filepath.Join(dir, "gopher.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/opt/gopher/gopher\n"), 0644); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	origUnit, origMarker := systemdUnitPath, managedModeHealMarker
	systemdUnitPath = unitPath
	managedModeHealMarker = filepath.Join(dir, "marker")
	t.Cleanup(func() { systemdUnitPath, managedModeHealMarker = origUnit, origMarker })

	withSudoStub(t, dir)

	// Calls the ungated helper directly — embedbin.Embedded() depends on
	// scripts/fetch-deps.sh having staged real binaries, which plain `go
	// test` never does; gating this test on it would make it pass or fail
	// based on repo state instead of the marker/patch logic being tested.
	ensureManagedModeAtStartup()

	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read patched unit: %v", err)
	}
	if !strings.Contains(string(got), "Environment=GOPHER_MANAGED=1") {
		t.Errorf("unit should have been patched, got:\n%s", got)
	}
	if _, err := os.Stat(managedModeHealMarker); err != nil {
		t.Error("marker file should exist after an attempt, successful or not")
	}
}

func TestEnsureManagedModeAtStartup_DoesNotRetryAfterMarkerExists(t *testing.T) {
	dir := t.TempDir()

	unitPath := filepath.Join(dir, "gopher.service")
	original := "[Service]\nExecStart=/opt/gopher/gopher\n"
	if err := os.WriteFile(unitPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	origUnit, origMarker := systemdUnitPath, managedModeHealMarker
	systemdUnitPath = unitPath
	managedModeHealMarker = filepath.Join(dir, "marker")
	t.Cleanup(func() { systemdUnitPath, managedModeHealMarker = origUnit, origMarker })

	// Pre-existing marker simulates "already tried once, didn't stick"
	// (e.g. GOPHER_MANAGED is still unset on this boot despite a prior
	// attempt) — deliberately no sudo stub on PATH, so a retry would fail
	// loudly rather than silently succeeding and masking the test's intent.
	if err := os.WriteFile(managedModeHealMarker, []byte("already tried\n"), 0644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	// Ungated helper — see the comment on the previous test for why.
	ensureManagedModeAtStartup()

	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if string(got) != original {
		t.Errorf("unit must be left untouched once the marker exists, got:\n%s", got)
	}
}
