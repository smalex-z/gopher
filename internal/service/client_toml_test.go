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

func TestMergeClientManagedConfig_PreservesUnmanagedAndRebuildsManaged(t *testing.T) {
	existing := `[client]
remote_addr = "router.example.com:2333"

[client.default_token]
default_token = "changeme"

[client.services.user-custom]
type = "tcp"
token = "custom"
local_addr = "localhost:9000"

# gopher-machine-start: mac123
[client.services.machine-mac123-ssh]
type = "tcp"
token = "old-token"
local_addr = "0.0.0.0:22"
# gopher-machine-end: mac123

# gopher-tunnel-start: oldtun
[client.services.tunnel-oldtun]
type = "tcp"
token = "oldtok"
local_addr = "localhost:9999"
# gopher-tunnel-end: oldtun

[client.services.tunnel-legacy]
token = "legacy"
local_addr = "127.0.0.1:7000"
`

	machine := &db.Machine{ID: "mac123", RatholeSSHToken: "new-ssh-token"}
	tunnels := []db.Tunnel{
		{ID: "tunA", MachineID: "mac123", RatholeToken: "tokA", LocalPort: 3000},
		{ID: "tunB", MachineID: "mac123", RatholeToken: "tokB", LocalPort: 3001},
	}

	updated, err := mergeClientManagedConfig(existing, machine, tunnels, "router.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(updated, "[client.services.user-custom]") {
		t.Fatalf("expected unmanaged custom section preserved:\n%s", updated)
	}
	if !strings.Contains(updated, "[client.default_token]") {
		t.Fatalf("expected unmanaged default_token section preserved:\n%s", updated)
	}
	if !strings.Contains(updated, "# gopher-machine-start: mac123") || !strings.Contains(updated, `token = "new-ssh-token"`) {
		t.Fatalf("expected rebuilt machine SSH managed section:\n%s", updated)
	}
	if !strings.Contains(updated, "[client.services.tunnel-tunA]") || !strings.Contains(updated, "[client.services.tunnel-tunB]") {
		t.Fatalf("expected rebuilt current tunnel sections:\n%s", updated)
	}
	if strings.Contains(updated, "old-token") || strings.Contains(updated, "[client.services.tunnel-oldtun]") || strings.Contains(updated, "[client.services.tunnel-legacy]") {
		t.Fatalf("expected stale managed tunnel/machine entries removed:\n%s", updated)
	}
}

func TestMergeClientManagedConfig_EmptyExistingRequiresHost(t *testing.T) {
	machine := &db.Machine{ID: "mac123", RatholeSSHToken: "tok-ssh"}
	_, err := mergeClientManagedConfig("", machine, nil, "")
	if err == nil {
		t.Fatal("expected error when existing config and host are both empty")
	}
}
