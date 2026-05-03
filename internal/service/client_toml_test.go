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

// Regression: the agent block was being stripped on every tunnel add because
// stripClientManagedSections matched the prefix "client.services.machine-"
// (catching both -ssh and -agent), but the merge only re-emitted -ssh. The
// agent's back-channel disappeared the moment an operator added their first
// tunnel, leaving the server-side bind with nothing connecting.
func TestMergeClientManagedConfig_PreservesAgentBlockWhenMachineHasAgentFields(t *testing.T) {
	existing := `[client]
remote_addr = "router.example.com:2333"

# gopher-machine-start: mac123
[client.services.machine-mac123-ssh]
type = "tcp"
token = "ssh-token"
local_addr = "0.0.0.0:22"
# gopher-machine-end: mac123

# gopher-machine-agent-start: mac123
[client.services.machine-mac123-agent]
type = "tcp"
token = "agent-token"
local_addr = "127.0.0.1:4322"
# gopher-machine-agent-end: mac123
`

	machine := &db.Machine{
		ID:                "mac123",
		RatholeSSHToken:   "ssh-token",
		AgentRatholeToken: "agent-token",
		AgentLocalPort:    4322,
	}
	updated, err := mergeClientManagedConfig(existing, machine, nil, "router.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(updated, "[client.services.machine-mac123-agent]") {
		t.Fatalf("agent service block was stripped — agent back-channel will not connect:\n%s", updated)
	}
	if !strings.Contains(updated, `local_addr = "127.0.0.1:4322"`) {
		t.Fatalf("expected agent local_addr preserved:\n%s", updated)
	}
	if !strings.Contains(updated, "# gopher-machine-agent-start: mac123") {
		t.Fatalf("expected agent marker preserved:\n%s", updated)
	}
}

// When the machine has no agent fields (legacy / un-migrated), the merge
// should not emit an agent block and should not error.
func TestMergeClientManagedConfig_OmitsAgentBlockForLegacyMachine(t *testing.T) {
	machine := &db.Machine{ID: "leg1", RatholeSSHToken: "ssh"}
	updated, err := mergeClientManagedConfig("", machine, nil, "router.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(updated, "machine-leg1-agent") {
		t.Fatalf("legacy machine without agent fields should not get an agent block:\n%s", updated)
	}
}

// Regression for the strip helper: orphaned agent markers (start without
// matching end) shouldn't leak into the output once stripping ran. The pre-fix
// strip helper didn't recognize "# gopher-machine-agent-" markers at all, so
// they survived as plain comment lines after the section body was removed.
func TestStripClientManagedMarkerBlocks_RemovesAgentMarkers(t *testing.T) {
	input := `[client]
remote_addr = "x"

# gopher-machine-agent-start: mac1
[client.services.machine-mac1-agent]
type = "tcp"
token = "t"
local_addr = "127.0.0.1:4322"
# gopher-machine-agent-end: mac1

[client.services.user-custom]
token = "custom"
`
	out := stripClientManagedMarkerBlocks(input)
	if strings.Contains(out, "gopher-machine-agent-") {
		t.Fatalf("agent markers should have been stripped:\n%s", out)
	}
	if !strings.Contains(out, "[client.services.user-custom]") {
		t.Fatalf("user section should be preserved:\n%s", out)
	}
}

// removeClientManagedSection now supports entryType="machine-agent" — exercise
// the new arm so a future regression in the agent removal path is caught.
func TestRemoveClientManagedSection_StripsAgentBlock(t *testing.T) {
	input := `# gopher-machine-agent-start: m1
[client.services.machine-m1-agent]
type = "tcp"
token = "t"
local_addr = "127.0.0.1:4322"
# gopher-machine-agent-end: m1

[client.services.user]
token = "u"
`
	out := removeClientManagedSection(input, "machine-agent", "m1")
	if strings.Contains(out, "machine-m1-agent") || strings.Contains(out, "gopher-machine-agent-") {
		t.Fatalf("expected agent block fully removed:\n%s", out)
	}
	if !strings.Contains(out, "[client.services.user]") {
		t.Fatalf("expected unrelated user block preserved:\n%s", out)
	}
}
