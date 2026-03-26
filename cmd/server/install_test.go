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

func TestBuildSudoers(t *testing.T) {
	content := buildSudoers("gopher", "/bin/systemctl", "/usr/bin/tee", "/bin/mkdir", "/usr/bin/pkill")

	required := []string{
		"gopher ALL=(ALL:ALL) NOPASSWD: /bin/systemctl",
		"gopher ALL=(ALL:ALL) NOPASSWD: /usr/bin/tee",
		"gopher ALL=(ALL:ALL) NOPASSWD: /bin/mkdir",
		"gopher ALL=(ALL:ALL) NOPASSWD: /usr/bin/pkill",
		"gopher ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv",
		"gopher ALL=(ALL:ALL) NOPASSWD: /bin/rm, /usr/bin/rm",
		"gopher ALL=(ALL:ALL) NOPASSWD: /usr/bin/chown, /bin/chown",
	}

	for _, line := range required {
		if !strings.Contains(content, line) {
			t.Fatalf("sudoers missing line: %s\nfull content:\n%s", line, content)
		}
	}

	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("sudoers should end with newline")
	}
}

func TestBuildSudoersEmpty(t *testing.T) {
	content := buildSudoers("gopher", "", "", "", "")
	// Should still have file operation commands even without paths
	if !strings.Contains(content, "gopher ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv") {
		t.Fatalf("expected limited sudo entries, got: %s", content)
	}
}

func TestBuildSudoersPartial(t *testing.T) {
	content := buildSudoers("gopher", "/bin/systemctl", "", "/bin/mkdir", "")
	// Should have systemctl and mkdir, plus file operations
	if !strings.Contains(content, "gopher ALL=(ALL:ALL) NOPASSWD: /bin/systemctl") {
		t.Fatalf("expected systemctl entry, got: %s", content)
	}
	if !strings.Contains(content, "gopher ALL=(ALL:ALL) NOPASSWD: /bin/mkdir") {
		t.Fatalf("expected mkdir entry, got: %s", content)
	}
	if !strings.Contains(content, "gopher ALL=(ALL:ALL) NOPASSWD: /bin/mv, /usr/bin/mv") {
		t.Fatalf("expected file operation entries, got: %s", content)
	}
}
