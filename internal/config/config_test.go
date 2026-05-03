package config

import (
	"strings"
	"testing"
	"time"

	"github.com/smalex-z/gopher/internal/db"
)

// ---- ValidateSubdomain ------------------------------------------------------

func TestValidateSubdomain(t *testing.T) {
	valid := []string{
		"app", "my-app", "myapp123", "a1b2c3",
		"web", "api-v2", "x",
	}
	for _, s := range valid {
		if err := ValidateSubdomain(s); err != nil {
			t.Errorf("ValidateSubdomain(%q) unexpected error: %v", s, err)
		}
	}

	invalid := []string{
		"", "-app", "app-", "My-App", "app.sub",
		"app_name", "APP", "has space",
	}
	for _, s := range invalid {
		if err := ValidateSubdomain(s); err == nil {
			t.Errorf("ValidateSubdomain(%q) expected error, got nil", s)
		}
	}
}

// ---- ValidatePort -----------------------------------------------------------

func TestValidatePort(t *testing.T) {
	validPorts := []int{1024, 8080, 65535, 3000, 9000}
	for _, p := range validPorts {
		if err := ValidatePort(p); err != nil {
			t.Errorf("ValidatePort(%d) unexpected error: %v", p, err)
		}
	}

	invalidPorts := []int{0, 22, 80, 443, 1023, 65536, -1}
	for _, p := range invalidPorts {
		if err := ValidatePort(p); err == nil {
			t.Errorf("ValidatePort(%d) expected error, got nil", p)
		}
	}
}

// ---- GenerateRatholeServerConfig --------------------------------------------

func TestGenerateRatholeServerConfig_Empty(t *testing.T) {
	out := GenerateRatholeServerConfig(nil, nil)
	if !strings.Contains(out, "[server]") {
		t.Error("missing [server] section")
	}
	if !strings.Contains(out, "bind_addr = \"0.0.0.0:2333\"") {
		t.Error("missing bind_addr")
	}
	// Placeholder added when no entries
	if !strings.Contains(out, "[server.services.placeholder]") {
		t.Error("expected placeholder when no tunnels or machines")
	}
}

func TestGenerateRatholeServerConfig_WithTunnel(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8080, RatholeToken: "tok1"},
	}
	out := GenerateRatholeServerConfig(nil, tunnels)

	if !strings.Contains(out, "# gopher-tunnel-start: t1") {
		t.Error("missing tunnel start marker")
	}
	if !strings.Contains(out, "# gopher-tunnel-end: t1") {
		t.Error("missing tunnel end marker")
	}
	if !strings.Contains(out, `token = "tok1"`) {
		t.Error("missing token")
	}
	if !strings.Contains(out, "bind_addr = \"0.0.0.0:8080\"") {
		t.Error("missing bind_addr for public tunnel")
	}
}

func TestGenerateRatholeServerConfig_PrivateTunnel(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 9000, RatholeToken: "tok", Private: true},
	}
	out := GenerateRatholeServerConfig(nil, tunnels)
	if !strings.Contains(out, "bind_addr = \"127.0.0.1:9000\"") {
		t.Errorf("private tunnel should bind 127.0.0.1, got:\n%s", out)
	}
}

func TestGenerateRatholeServerConfig_UDPTunnel(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "udp1", RatholePort: 5000, RatholeToken: "tok", Transport: "udp"},
	}
	out := GenerateRatholeServerConfig(nil, tunnels)
	if !strings.Contains(out, `type = "udp"`) {
		t.Error("UDP tunnel should include type = \"udp\"")
	}
}

func TestGenerateRatholeServerConfig_WithMachine(t *testing.T) {
	machines := []db.Machine{
		{ID: "m1", TunnelPort: 2222, RatholeSSHToken: "ssh-tok"},
	}
	out := GenerateRatholeServerConfig(machines, nil)

	if !strings.Contains(out, "# gopher-machine-start: m1") {
		t.Error("missing machine start marker")
	}
	if !strings.Contains(out, `token = "ssh-tok"`) {
		t.Error("missing machine token")
	}
	if !strings.Contains(out, "bind_addr = \"127.0.0.1:2222\"") {
		t.Error("machine should default to private (127.0.0.1) when PublicSSH=false")
	}
}

func TestGenerateRatholeServerConfig_PublicSSHMachine(t *testing.T) {
	machines := []db.Machine{
		{ID: "m1", TunnelPort: 2222, RatholeSSHToken: "ssh-tok", PublicSSH: true},
	}
	out := GenerateRatholeServerConfig(machines, nil)
	if !strings.Contains(out, "bind_addr = \"0.0.0.0:2222\"") {
		t.Errorf("public SSH machine should bind 0.0.0.0, got:\n%s", out)
	}
}

func TestGenerateRatholeServerConfig_SkipsMachineWithoutToken(t *testing.T) {
	machines := []db.Machine{
		{ID: "m-no-token", TunnelPort: 2222, RatholeSSHToken: ""},
		{ID: "m-no-port", TunnelPort: 0, RatholeSSHToken: "tok"},
	}
	out := GenerateRatholeServerConfig(machines, nil)
	if strings.Contains(out, "m-no-token") || strings.Contains(out, "m-no-port") {
		t.Error("machines without token or port should be skipped")
	}
}

func TestGenerateRatholeServerConfig_TokenFallsBackToID(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t-legacy", RatholePort: 7000, RatholeToken: ""},
	}
	out := GenerateRatholeServerConfig(nil, tunnels)
	if !strings.Contains(out, `token = "t-legacy"`) {
		t.Error("tunnel with empty RatholeToken should fall back to ID as token")
	}
}

func TestGenerateRatholeServerConfig_NoPlaceholderWhenHasMachines(t *testing.T) {
	machines := []db.Machine{
		{ID: "m1", TunnelPort: 2222, RatholeSSHToken: "tok"},
	}
	out := GenerateRatholeServerConfig(machines, nil)
	if strings.Contains(out, "placeholder") {
		t.Error("should not include placeholder when machines are present")
	}
}

// ---- GenerateMachineSSHClientConfig -----------------------------------------

func TestGenerateMachineSSHClientConfig(t *testing.T) {
	m := &db.Machine{ID: "m1", RatholeSSHToken: "my-token"}
	out := GenerateMachineSSHClientConfig("vps.example.com", m)

	if !strings.Contains(out, `remote_addr = "vps.example.com:2333"`) {
		t.Error("missing remote_addr")
	}
	if !strings.Contains(out, "# gopher-machine-start: m1") {
		t.Error("missing machine start marker")
	}
	if !strings.Contains(out, `token = "my-token"`) {
		t.Error("missing token")
	}
	if !strings.Contains(out, `local_addr = "0.0.0.0:22"`) {
		t.Error("missing local_addr for SSH")
	}
}

// ---- GenerateClientConfig ---------------------------------------------------

func TestGenerateClientConfig(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholeToken: "tok1", LocalPort: 8080},
		{ID: "t2", RatholeToken: "tok2", LocalPort: 9090},
	}
	out, err := GenerateClientConfig("vps.example.com", tunnels)
	if err != nil {
		t.Fatalf("GenerateClientConfig: %v", err)
	}

	if !strings.Contains(out, `remote_addr = "vps.example.com:2333"`) {
		t.Error("missing remote_addr")
	}
	if !strings.Contains(out, "tunnel-t1") {
		t.Error("missing tunnel t1")
	}
	if !strings.Contains(out, "tunnel-t2") {
		t.Error("missing tunnel t2")
	}
	if !strings.Contains(out, "127.0.0.1:8080") {
		t.Error("missing local_addr for t1")
	}
}

func TestGenerateClientConfig_Empty(t *testing.T) {
	out, err := GenerateClientConfig("vps.example.com", nil)
	if err != nil {
		t.Fatalf("GenerateClientConfig (empty): %v", err)
	}
	if !strings.Contains(out, `remote_addr = "vps.example.com:2333"`) {
		t.Error("missing remote_addr for empty config")
	}
}

// ---- extractPortFromBindAddr ------------------------------------------------

func TestExtractPortFromBindAddr(t *testing.T) {
	tests := []struct {
		addr string
		want int
	}{
		{"0.0.0.0:8080", 8080},
		{"127.0.0.1:2222", 2222},
		{"0.0.0.0:65535", 65535},
		{"", 0},
		{"noport", 0},
		{"host:notanumber", 0},
		{"a:b:c", 0},
	}
	for _, tt := range tests {
		got := extractPortFromBindAddr(tt.addr)
		if got != tt.want {
			t.Errorf("extractPortFromBindAddr(%q) = %d, want %d", tt.addr, got, tt.want)
		}
	}
}

// ---- parseRatholeConfig -----------------------------------------------------

func TestParseRatholeConfig_BasicTunnel(t *testing.T) {
	cfg := `
[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: t1
[server.services.tunnel-t1]
token = "tok1"
bind_addr = "0.0.0.0:8080"
# gopher-tunnel-end: t1
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	entry, ok := result.Tunnels["t1"]
	if !ok {
		t.Fatal("expected tunnel t1 in result")
	}
	if entry.Token != "tok1" {
		t.Errorf("Token = %q, want %q", entry.Token, "tok1")
	}
	if entry.BindAddr != "0.0.0.0:8080" {
		t.Errorf("BindAddr = %q, want %q", entry.BindAddr, "0.0.0.0:8080")
	}
}

func TestParseRatholeConfig_BasicMachine(t *testing.T) {
	cfg := `
# gopher-machine-start: m1
[server.services.machine-m1-ssh]
token = "ssh-tok"
bind_addr = "127.0.0.1:2222"
# gopher-machine-end: m1
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}
	entry, ok := result.Machines["m1"]
	if !ok {
		t.Fatal("expected machine m1")
	}
	if entry.Token != "ssh-tok" {
		t.Errorf("Token = %q, want %q", entry.Token, "ssh-tok")
	}
}

// gopher-agent back-channel entries share the machine id but use a distinct
// marker prefix. Parser must recognize them, store separately, and not flag
// "machine" + "machine-agent" with the same id as duplicates.
func TestParseRatholeConfig_MachineAgentEntry(t *testing.T) {
	cfg := `
# gopher-machine-start: m1
[server.services.machine-m1-ssh]
token = "ssh-tok"
bind_addr = "127.0.0.1:2222"
# gopher-machine-end: m1

# gopher-machine-agent-start: m1
[server.services.machine-m1-agent]
token = "agent-tok"
bind_addr = "127.0.0.1:9001"
# gopher-machine-agent-end: m1
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", result.Errors)
	}

	if _, ok := result.Machines["m1"]; !ok {
		t.Fatal("expected machine m1 in Machines map")
	}
	agent, ok := result.MachineAgents["m1"]
	if !ok {
		t.Fatal("expected machine m1 in MachineAgents map")
	}
	if agent.Token != "agent-tok" {
		t.Errorf("agent Token = %q, want %q", agent.Token, "agent-tok")
	}
	if agent.BindAddr != "127.0.0.1:9001" {
		t.Errorf("agent BindAddr = %q, want %q", agent.BindAddr, "127.0.0.1:9001")
	}
	if len(result.DuplicateIDs) > 0 {
		t.Errorf("machine + machine-agent with same id should not flag duplicates, got %v", result.DuplicateIDs)
	}
}

func TestParseRatholeConfig_DuplicateID(t *testing.T) {
	cfg := `
# gopher-tunnel-start: t1
token = "tok1"
bind_addr = "0.0.0.0:8080"
# gopher-tunnel-end: t1

# gopher-tunnel-start: t1
token = "tok2"
bind_addr = "0.0.0.0:8081"
# gopher-tunnel-end: t1
`
	result := parseRatholeConfig(cfg)
	if len(result.DuplicateIDs) == 0 {
		t.Error("expected duplicate ID to be detected")
	}
}

func TestParseRatholeConfig_UnclosedMarker(t *testing.T) {
	cfg := `
# gopher-tunnel-start: t1
token = "tok"
bind_addr = "0.0.0.0:8080"
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) == 0 {
		t.Error("expected error for unclosed marker")
	}
}

func TestParseRatholeConfig_OrphanedEndMarker(t *testing.T) {
	cfg := `
# gopher-tunnel-end: t1
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) == 0 {
		t.Error("expected error for orphaned end marker")
	}
}

func TestParseRatholeConfig_MismatchedMarkers(t *testing.T) {
	cfg := `
# gopher-tunnel-start: t1
token = "tok"
# gopher-tunnel-end: t2
`
	result := parseRatholeConfig(cfg)
	if len(result.Errors) == 0 {
		t.Error("expected error for mismatched start/end IDs")
	}
}

// ---- ValidateRatholeConfig --------------------------------------------------

func TestValidateRatholeConfig_MatchesDB(t *testing.T) {
	machines := []db.Machine{
		{ID: "m1", TunnelPort: 2222, RatholeSSHToken: "ssh-tok"},
	}
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8080, RatholeToken: "tok1"},
	}
	cfg := GenerateRatholeServerConfig(machines, tunnels)

	result := ValidateRatholeConfig(cfg, machines, tunnels)
	if !result.Valid {
		t.Errorf("expected valid config, errors: %v", result.Errors)
	}
}

func TestValidateRatholeConfig_OrphanedEntry(t *testing.T) {
	cfg := `
[server]
bind_addr = "0.0.0.0:2333"
# gopher-tunnel-start: orphan
[server.services.tunnel-orphan]
token = "tok"
bind_addr = "0.0.0.0:9999"
# gopher-tunnel-end: orphan
`
	result := ValidateRatholeConfig(cfg, nil, nil)
	if result.Valid {
		t.Error("expected invalid — config has tunnel not in DB")
	}
	if len(result.Orphans) == 0 {
		t.Error("expected orphan to be reported")
	}
}

func TestValidateRatholeConfig_MissingEntry(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8080, RatholeToken: "tok1"},
	}
	result := ValidateRatholeConfig("[server]\nbind_addr = \"0.0.0.0:2333\"\n", nil, tunnels)
	if result.Valid {
		t.Error("expected invalid — DB has tunnel not in config")
	}
	if len(result.Missing) == 0 {
		t.Error("expected missing entry to be reported")
	}
}

func TestValidateRatholeConfig_TokenMismatch(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8080, RatholeToken: "correct-token"},
	}
	cfg := `
[server]
bind_addr = "0.0.0.0:2333"
# gopher-tunnel-start: t1
[server.services.tunnel-t1]
token = "wrong-token"
bind_addr = "0.0.0.0:8080"
# gopher-tunnel-end: t1
`
	result := ValidateRatholeConfig(cfg, nil, tunnels)
	if result.Valid {
		t.Error("expected invalid due to token mismatch")
	}
	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "Token mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Token mismatch error, got: %v", result.Errors)
	}
}

func TestValidateRatholeConfig_PortMismatch(t *testing.T) {
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8080, RatholeToken: "tok"},
	}
	cfg := `
[server]
bind_addr = "0.0.0.0:2333"
# gopher-tunnel-start: t1
[server.services.tunnel-t1]
token = "tok"
bind_addr = "0.0.0.0:9999"
# gopher-tunnel-end: t1
`
	result := ValidateRatholeConfig(cfg, nil, tunnels)
	if result.Valid {
		t.Error("expected invalid due to port mismatch")
	}
}

func TestValidateRatholeConfig_SkipsMachineWithoutToken(t *testing.T) {
	// Machines without SSH token should not be required in config
	machines := []db.Machine{
		{ID: "m-no-tok", TunnelPort: 2222, RatholeSSHToken: ""},
	}
	result := ValidateRatholeConfig("[server]\nbind_addr = \"0.0.0.0:2333\"\n", machines, nil)
	if !result.Valid {
		t.Errorf("machine without token should be excluded from validation, errors: %v", result.Errors)
	}
}

// ---- Roundtrip: generate then validate --------------------------------------

func TestRoundtrip_GenerateAndValidate(t *testing.T) {
	now := time.Now()
	machines := []db.Machine{
		{ID: "m1", TunnelPort: 2222, RatholeSSHToken: "tok-m1"},
		{ID: "m2", TunnelPort: 2223, RatholeSSHToken: "tok-m2", PublicSSH: true},
	}
	tunnels := []db.Tunnel{
		{ID: "t1", RatholePort: 8001, RatholeToken: "tok-t1", CreatedAt: now, UpdatedAt: now},
		{ID: "t2", RatholePort: 8002, RatholeToken: "tok-t2", Private: true, CreatedAt: now, UpdatedAt: now},
		{ID: "t3", RatholePort: 8003, RatholeToken: "tok-t3", Transport: "udp", CreatedAt: now, UpdatedAt: now},
	}

	cfg := GenerateRatholeServerConfig(machines, tunnels)
	result := ValidateRatholeConfig(cfg, machines, tunnels)

	if !result.Valid {
		t.Errorf("roundtrip validation failed: %v", result.Errors)
	}
	if len(result.Orphans) > 0 {
		t.Errorf("unexpected orphans: %v", result.Orphans)
	}
	if len(result.Missing) > 0 {
		t.Errorf("unexpected missing: %v", result.Missing)
	}
}
