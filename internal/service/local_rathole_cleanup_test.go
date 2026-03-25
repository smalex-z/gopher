package service

import (
	"strings"
	"testing"
)

func TestBuildMachineClientCleanupScript_ContainsRequiredCleanupSteps(t *testing.T) {
	script := buildMachineClientCleanupScript("/home/ubuntu", "/tmp/.gopher-remove-rathole.sh")

	required := []string{
		"systemctl stop rathole-client",
		"systemctl disable rathole-client",
		"rm -f /etc/systemd/system/rathole-client.service",
		"rm -f \"$HOME_DIR/.config/systemd/user/rathole-client.service\"",
		"rm -f /etc/rathole/client.toml",
		"rm -f \"$HOME_DIR/.config/rathole/client.toml\"",
		"rm -f /usr/local/bin/rathole",
		"rm -f \"$HOME_DIR/.local/bin/rathole\"",
	}

	for _, piece := range required {
		if !strings.Contains(script, piece) {
			t.Fatalf("cleanup script missing %q\n%s", piece, script)
		}
	}
}

func TestBuildMachineClientCleanupScript_QuotesPaths(t *testing.T) {
	script := buildMachineClientCleanupScript("/home/user with space", "/tmp/gopher cleanup.sh")
	if !strings.Contains(script, `HOME_DIR="/home/user with space"`) {
		t.Fatalf("expected quoted HOME_DIR assignment, got:\n%s", script)
	}
	if !strings.Contains(script, `rm -f "/tmp/gopher cleanup.sh"`) {
		t.Fatalf("expected quoted script cleanup path, got:\n%s", script)
	}
}
