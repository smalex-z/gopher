package main

import (
	"strings"
	"testing"
)

func TestBuildServiceUnit(t *testing.T) {
	unit := buildServiceUnit("gopher", "/opt/gopher/gopher", "/var/lib/gopher/gopher.db")

	if !strings.Contains(unit, "User=gopher") {
		t.Fatalf("service unit missing user: %q", unit)
	}
	if !strings.Contains(unit, "ExecStart=/opt/gopher/gopher --db /var/lib/gopher/gopher.db") {
		t.Fatalf("service unit missing exec start: %q", unit)
	}
	if !strings.Contains(unit, "WantedBy=multi-user.target") {
		t.Fatalf("service unit missing install target: %q", unit)
	}
}

// buildSudoers now grants full passwordless sudo (NOPASSWD: ALL) to match
// the client-side bootstrap model. Anchor the test to that exact line — it's
// the entire content surface, so a regression to a partial allowlist would
// fail loudly here.
func TestBuildSudoers(t *testing.T) {
	content := buildSudoers("gopher")
	want := "gopher ALL=(ALL) NOPASSWD: ALL"
	if !strings.Contains(content, want) {
		t.Fatalf("sudoers missing %q\nfull content:\n%s", want, content)
	}
	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("sudoers should end with newline")
	}
}

// The "narrow" bootstrap sudoers must never contain a shell or downloader —
// NOPASSWD bash is NOPASSWD:ALL with extra steps. The package-manager entry
// carries SETENV so the firewall takeover can pass
// DEBIAN_FRONTEND=noninteractive on the sudo command line instead.
func TestBuildBootstrapSudoers_NoShellNoDownloader(t *testing.T) {
	out := buildBootstrapSudoers("alice")
	for _, banned := range []string{"bash", "/bin/sh", "curl", "wget"} {
		if strings.Contains(out, banned) {
			t.Errorf("bootstrap sudoers contains %q:\n%s", banned, out)
		}
	}
	if !strings.Contains(out, "NOPASSWD:SETENV: ") {
		t.Errorf("package-manager entry should carry SETENV::\n%s", out)
	}
	if !strings.Contains(out, "alice ALL=(ALL:ALL) NOPASSWD: /bin/systemctl") {
		t.Errorf("expected systemctl entry:\n%s", out)
	}
}
