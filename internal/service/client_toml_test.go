package service

import (
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/config"
	"github.com/smalex-z/gopher/internal/db"
)

func TestBuildClientTunnelSection_UsesServerStyleDelimiters(t *testing.T) {
	tunnel := &db.Tunnel{ID: "tun123", RatholeToken: "tok123", LocalPort: 8080}
	section := buildClientTunnelSection(tunnel)

	if !strings.Contains(section, "# gopher-tunnel-start: tun123") {
		t.Fatalf("missing tunnel start marker:\n%s", section)
	}
	if !strings.Contains(section, "# gopher-tunnel-end: tun123") {
		t.Fatalf("missing tunnel end marker:\n%s", section)
	}
	if !strings.Contains(section, `[client.services.tunnel-tun123]`) {
		t.Fatalf("missing tunnel section header:\n%s", section)
	}
}

func TestGenerateMachineSSHClientConfig_UsesServerStyleDelimiters(t *testing.T) {
	machine := &db.Machine{ID: "mac123", RatholeSSHToken: "ssh-token"}
	cfg := config.GenerateMachineSSHClientConfig("router.example.com", machine)

	if !strings.Contains(cfg, "# gopher-machine-start: mac123") {
		t.Fatalf("missing machine start marker:\n%s", cfg)
	}
	if !strings.Contains(cfg, "# gopher-machine-end: mac123") {
		t.Fatalf("missing machine end marker:\n%s", cfg)
	}
}

func TestRemoveClientManagedSection_RemovesMarkersAndBlock(t *testing.T) {
	content := `[client]
remote_addr = "router.example.com:2333"

# gopher-tunnel-start: tun123
[client.services.tunnel-tun123]
type = "tcp"
token = "tok123"
local_addr = "localhost:8080"
# gopher-tunnel-end: tun123

[client.services.user-custom]
type = "tcp"
token = "custom"
local_addr = "localhost:9000"
`

	updated := removeClientManagedSection(content, "tunnel", "tun123")
	if strings.Contains(updated, "tun123") {
		t.Fatalf("expected tun123 block and markers removed:\n%s", updated)
	}
	if !strings.Contains(updated, "user-custom") {
		t.Fatalf("expected unrelated user block preserved:\n%s", updated)
	}
}
