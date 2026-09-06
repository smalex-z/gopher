package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runManagedKeyShell executes the exact shell string setManagedKeyShell builds,
// with HOME pointed at a temp dir so ~/.ssh resolves there. Returns the
// resulting authorized_keys contents (or "" if absent).
func runManagedKeyShell(t *testing.T, home, pubKey string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", setManagedKeyShell(pubKey))
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell failed: %v: %s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read authorized_keys: %v", err)
	}
	return string(b)
}

// A crafted comment containing $(...) must NOT execute — this is the shell
// injection the %q→single-quote fix closes.
func TestSetManagedKeyShell_NoInjection(t *testing.T) {
	home := t.TempDir()
	sentinel := filepath.Join(home, "PWNED")
	// Valid ed25519-shaped line whose comment is a command substitution.
	pub := "ssh-ed25519 AAAAC3NzaC1blobblobblob $(touch " + sentinel + ")"

	got := runManagedKeyShell(t, home, pub)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("INJECTION: sentinel file was created — command substitution fired")
	}
	want := "ssh-ed25519 AAAAC3NzaC1blobblobblob gopher-managed\n"
	if got != want {
		t.Fatalf("authorized_keys = %q, want %q", got, want)
	}
}

// Backticks are the other substitution form; also must stay inert.
func TestSetManagedKeyShell_NoBacktickInjection(t *testing.T) {
	home := t.TempDir()
	sentinel := filepath.Join(home, "PWNED2")
	pub := "ssh-ed25519 AAAAC3NzaC1blob `touch " + sentinel + "`"

	runManagedKeyShell(t, home, pub)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("INJECTION: backtick substitution fired")
	}
}

// The core invariant: exactly one managed key, operator keys untouched, prior
// managed key (including a CRLF-terminated one) removed.
func TestSetManagedKeyShell_SwapsAndPreserves(t *testing.T) {
	home := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	// Operator key (no marker), an old managed key, and a CRLF-terminated managed
	// key that a naive end-anchor would miss.
	seed := "ssh-rsa OPERATORBLOB me@laptop\n" +
		"ssh-ed25519 OLDMANAGEDBLOB gopher-managed\n" +
		"ssh-ed25519 CRLFMANAGEDBLOB gopher-managed\r\n"
	if err := os.WriteFile(filepath.Join(ssh, "authorized_keys"), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	got := runManagedKeyShell(t, home, "ssh-ed25519 NEWMANAGEDBLOB whatever@host")

	if !strings.Contains(got, "ssh-rsa OPERATORBLOB me@laptop") {
		t.Errorf("operator key was removed:\n%s", got)
	}
	if strings.Contains(got, "OLDMANAGEDBLOB") || strings.Contains(got, "CRLFMANAGEDBLOB") {
		t.Errorf("a prior managed key survived (CRLF case?):\n%s", got)
	}
	if n := strings.Count(got, " gopher-managed"); n != 1 {
		t.Errorf("want exactly 1 managed key, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, "ssh-ed25519 NEWMANAGEDBLOB gopher-managed") {
		t.Errorf("new managed key missing/mistagged:\n%s", got)
	}
}
