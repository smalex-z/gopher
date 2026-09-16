package service

import (
	"strings"
	"testing"
)

// A tunnel with aliases must render one Caddy site block whose address lists
// every hostname (primary + aliases), sharing a single reverse_proxy.
func TestBuildTunnelCaddyBlock_MultiHost(t *testing.T) {
	block := buildTunnelCaddyBlock("members", "example.com", []string{"member", "www"}, 1030, false, false, "", false, false)
	for _, h := range []string{"members.example.com", "member.example.com", "www.example.com"} {
		if !strings.Contains(block, h) {
			t.Errorf("block missing hostname %q:\n%s", h, block)
		}
	}
	if !strings.Contains(block, "members.example.com, member.example.com, www.example.com {") {
		t.Errorf("addresses not comma-joined on one block:\n%s", block)
	}
	if n := strings.Count(block, "reverse_proxy"); n != 1 {
		t.Errorf("want exactly 1 reverse_proxy, got %d:\n%s", n, block)
	}
}

// No aliases → unchanged single-host block.
func TestBuildTunnelCaddyBlock_SingleHost(t *testing.T) {
	block := buildTunnelCaddyBlock("photos", "example.com", nil, 1030, false, false, "", false, false)
	if !strings.Contains(block, "photos.example.com {") {
		t.Errorf("single-host block malformed:\n%s", block)
	}
	if strings.Contains(block, ",") {
		t.Errorf("single-host block should have no comma address:\n%s", block)
	}
}
