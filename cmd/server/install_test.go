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
		"gopher ALL=(ALL:ALL) NOPASSWD: ALL",
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
