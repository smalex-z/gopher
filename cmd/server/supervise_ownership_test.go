package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression for the uclaacm.com outage: an edge whose /opt/gopher predates
// gopher install's chownRecursive step (or never ran it) has BinDir's parent
// owned root:root — the plain os.MkdirAll in embedbin.ExtractAll fails with
// permission denied, and MigrateEdgeLayout had already stopped the legacy
// caddy/rathole units by that point, so the edge goes fully down with no
// supervised replacement. This must self-heal via the sudo every gopher
// install already has, not require someone to SSH in and chown it by hand.

func TestEnsureDirWritableBySelf_FastPathWhenAlreadyWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := ensureDirWritableBySelf(dir); err != nil {
		t.Fatalf("ensureDirWritableBySelf: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir should exist after the plain MkdirAll fast path: %v", err)
	}
}

func TestEnsureDirWritableBySelf_SudoFallbackOnPermissionDenied(t *testing.T) {
	// Simulate the exact failure: a parent directory the current user can't
	// write into, so the plain os.MkdirAll fast path fails and the function
	// must fall back to the sudo path.
	parent := t.TempDir()
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatalf("chmod parent read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) }) // let t.TempDir() clean up
	target := filepath.Join(parent, "bin")

	// Stand-in `sudo` on PATH. Real sudo bypasses the permission bits
	// entirely by running as root; this stub simulates exactly that one
	// effect (via a side-channel env var naming the locked directory —
	// simplest way to unlock it from POSIX sh without parsing which
	// argument is the path) and then runs the real mkdir/chown, proving
	// ensureDirWritableBySelf issues the right commands and that they
	// actually land, not just that *some* command ran.
	t.Setenv("FAKE_SUDO_UNLOCK_DIR", parent)
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "sudo")
	script := "#!/bin/sh\n" +
		"chmod u+w \"$FAKE_SUDO_UNLOCK_DIR\"\n" +
		"if [ \"$1\" = -n ]; then shift; fi\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(stub, []byte(script), 0755); err != nil {
		t.Fatalf("write sudo stub: %v", err)
	}
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))

	if err := ensureDirWritableBySelf(target); err != nil {
		t.Fatalf("ensureDirWritableBySelf sudo fallback: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Fatalf("dir should exist after the sudo fallback: %v", err)
	}
}

func TestEnsureDirWritableBySelf_PropagatesSudoFailure(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatalf("chmod parent read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	target := filepath.Join(parent, "bin")

	// No sudo stub on PATH at all — mirrors a box where sudo genuinely
	// can't do this (denied, missing), which must surface as a real error,
	// not a silent "looked fine" no-op.
	t.Setenv("PATH", t.TempDir())

	err := ensureDirWritableBySelf(target)
	if err == nil {
		t.Fatal("expected an error when the sudo fallback itself can't run")
	}
	if !strings.Contains(err.Error(), "sudo mkdir") {
		t.Errorf("error should name the failing step, got: %v", err)
	}
}
