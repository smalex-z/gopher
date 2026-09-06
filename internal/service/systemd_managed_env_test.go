package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The self-update path (unlike `gopher install`) never used to touch the
// systemd unit at all, so any pre-v0.1.0 install upgrading via the dashboard
// button silently never gained GOPHER_MANAGED=1 — see the comment on the
// Apply() call site. These tests cover the pure insertion logic plus the
// file-level patch (with sudo replaced by a plain-copy stand-in, since the
// test process isn't root).

func TestEnsureManagedEnvLine_InsertsWhenMissing(t *testing.T) {
	unit := "[Unit]\nDescription=Gopher Tunnel Gateway\n\n[Service]\nType=simple\nUser=gopher\nExecStart=/opt/gopher/gopher --db /var/lib/gopher/gopher.db\nRestart=always\n\n[Install]\nWantedBy=multi-user.target\n"
	got, changed := ensureManagedEnvLine(unit)
	if !changed {
		t.Fatal("expected changed=true when GOPHER_MANAGED is absent")
	}
	if !strings.Contains(got, "[Service]\nEnvironment=GOPHER_MANAGED=1\n") {
		t.Errorf("Environment line not inserted right after [Service]:\n%s", got)
	}
	// Rest of the file must survive untouched.
	for _, want := range []string{"Type=simple", "User=gopher", "ExecStart=/opt/gopher/gopher --db /var/lib/gopher/gopher.db", "[Install]"} {
		if !strings.Contains(got, want) {
			t.Errorf("patched unit lost %q:\n%s", want, got)
		}
	}
}

func TestEnsureManagedEnvLine_NoOpWhenPresent(t *testing.T) {
	unit := "[Service]\nEnvironment=GOPHER_MANAGED=1\nExecStart=/opt/gopher/gopher\n"
	got, changed := ensureManagedEnvLine(unit)
	if changed {
		t.Fatal("expected changed=false when GOPHER_MANAGED already present")
	}
	if got != unit {
		t.Errorf("unit content should be untouched:\n%s", got)
	}
}

func TestEnsureManagedEnvLine_IdempotentAcrossRepeatedApplies(t *testing.T) {
	unit := "[Service]\nExecStart=/opt/gopher/gopher\n"
	once, changed := ensureManagedEnvLine(unit)
	if !changed {
		t.Fatal("first pass should change the unit")
	}
	twice, changed := ensureManagedEnvLine(once)
	if changed {
		t.Fatal("second pass over already-patched content must be a no-op")
	}
	if once != twice {
		t.Error("repeated patching must be idempotent")
	}
}

func TestEnsureManagedEnvLine_UnrecognizedUnitLeftAlone(t *testing.T) {
	unit := "this is not a systemd unit file\n"
	got, changed := ensureManagedEnvLine(unit)
	if changed || got != unit {
		t.Error("a unit with no [Service] section must be left untouched, not guessed at")
	}
}

// TestPatchSystemdManagedEnv_WritesAndDaemonReloads exercises the file-level
// patch with sudo/systemctl stubbed out via PATH — the test process isn't
// root and must never touch the real systemd unit.
func TestPatchSystemdManagedEnv_WritesAndDaemonReloads(t *testing.T) {
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "gopher.service")
	original := "[Unit]\nDescription=x\n\n[Service]\nExecStart=/opt/gopher/gopher\n"
	if err := os.WriteFile(unitPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed unit file: %v", err)
	}

	origPath, origUnit := systemdUnitPath, unitPath
	systemdUnitPath = origUnit
	t.Cleanup(func() { systemdUnitPath = origPath })

	// Stand-in `sudo` on PATH: `sudo -n tee <path>` copies stdin to the path
	// with no privilege step (test isn't root); `sudo -n systemctl
	// daemon-reload` is a no-op. Mirrors the pattern other tests in this repo
	// use to keep sudo-shaped code paths testable without touching the host.
	stub := filepath.Join(dir, "sudo")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nif [ \"$2\" = tee ]; then exec tee \"$3\"; fi\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write sudo stub: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	if err := patchSystemdManagedEnv(); err != nil {
		t.Fatalf("patchSystemdManagedEnv: %v", err)
	}
	got, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read patched unit: %v", err)
	}
	if !strings.Contains(string(got), "Environment=GOPHER_MANAGED=1") {
		t.Errorf("patched file missing the env line:\n%s", got)
	}

	// Second call must be a true no-op — no error, and it must not need the
	// tee stub to succeed again (would fail loudly if it tried to re-invoke
	// sudo tee with a bad exit path).
	if err := patchSystemdManagedEnv(); err != nil {
		t.Fatalf("second call should no-op cleanly: %v", err)
	}
}
