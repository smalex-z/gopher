package unit

import (
"strings"
"testing"
"time"

"github.com/smalex-z/gopher/internal/config"
"github.com/smalex-z/gopher/internal/db"
)

// Test generation creates valid config with markers
func TestGenerateRatholeServerConfig(t *testing.T) {
machines := []db.Machine{
{
ID:              "test-mac",
TunnelPort:      10000,
RatholeSSHToken: "ssh-token-123",
CreatedAt:       time.Now(),
UpdatedAt:       time.Now(),
},
}

tunnels := []db.Tunnel{
{
ID:           "test-tun",
RatholePort:  20000,
RatholeToken: "tun-token-456",
CreatedAt:    time.Now(),
UpdatedAt:    time.Now(),
},
}

cfg := config.GenerateRatholeServerConfig(machines, tunnels)

// Verify markers are present
if !strings.Contains(cfg, "# gopher-machine-start: test-mac") {
t.Error("Missing machine start marker")
}
if !strings.Contains(cfg, "# gopher-machine-end: test-mac") {
t.Error("Missing machine end marker")
}
if !strings.Contains(cfg, "# gopher-tunnel-start: test-tun") {
t.Error("Missing tunnel start marker")
}
if !strings.Contains(cfg, "# gopher-tunnel-end: test-tun") {
t.Error("Missing tunnel end marker")
}

// Verify content
if !strings.Contains(cfg, "ssh-token-123") {
t.Error("Missing token in config")
}
if !strings.Contains(cfg, "0.0.0.0:10000") {
t.Error("Missing bind address in config")
}
}

// Test duplicate IDs are detected
func TestValidateDuplicates(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: dup-id
[server.services.tunnel-dup-1]
token = "tok1"
bind_addr = "0.0.0.0:20000"
# gopher-tunnel-end: dup-id

# gopher-tunnel-start: dup-id
[server.services.tunnel-dup-2]
token = "tok2"
bind_addr = "0.0.0.0:20001"
# gopher-tunnel-end: dup-id
`

tunnels := []db.Tunnel{
{ID: "dup-id", RatholePort: 20000, RatholeToken: "tok1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
}

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, tunnels)

if result.Valid {
t.Error("Should detect duplicates")
}
if len(result.Duplicates) == 0 {
t.Error("Duplicates list should not be empty")
}
if len(result.Errors) == 0 {
t.Error("Should have error messages")
}
}

// Test orphaned entries are detected
func TestValidateOrphans(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: orphan-id
[server.services.tunnel-orphan-id]
token = "token"
bind_addr = "0.0.0.0:20000"
# gopher-tunnel-end: orphan-id
`

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{})

if result.Valid {
t.Error("Should reject orphaned entries")
}
if len(result.Orphans) == 0 {
t.Error("Should list orphaned entries")
}
}

// Test missing entries are detected
func TestValidateMissing(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"
`

tunnels := []db.Tunnel{
{ID: "missing-id", RatholePort: 20000, RatholeToken: "token", CreatedAt: time.Now(), UpdatedAt: time.Now()},
}

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, tunnels)

if result.Valid {
t.Error("Should detect missing entries")
}
if len(result.Missing) == 0 {
t.Error("Should list missing entries")
}
}

// Test valid config passes all checks
func TestValidateValid(t *testing.T) {
tunnel := db.Tunnel{
ID:           "test-id",
RatholePort:  20000,
RatholeToken: "test-token",
CreatedAt:    time.Now(),
UpdatedAt:    time.Now(),
}

cfg := config.GenerateRatholeServerConfig([]db.Machine{}, []db.Tunnel{tunnel})

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{tunnel})

if !result.Valid {
t.Errorf("Valid config should pass validation. Errors: %v", result.Errors)
}
}

// Test backward compatibility (empty token uses ID)
func TestValidateBackwardCompat(t *testing.T) {
tunnel := db.Tunnel{
ID:           "old-tunnel",
RatholePort:  20000,
RatholeToken: "", // Empty token
CreatedAt:    time.Now(),
UpdatedAt:    time.Now(),
}

// Config was generated with ID as token
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: old-tunnel
[server.services.tunnel-old-tunnel]
token = "old-tunnel"
bind_addr = "0.0.0.0:20000"
# gopher-tunnel-end: old-tunnel
`

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{tunnel})

if !result.Valid {
t.Errorf("Should accept backward compat token. Errors: %v", result.Errors)
}
}

// Test generator skips incomplete entries
func TestGenerateSkipsIncomplete(t *testing.T) {
machines := []db.Machine{
{ID: "complete", TunnelPort: 10000, RatholeSSHToken: "token"},
{ID: "no-token", TunnelPort: 10001, RatholeSSHToken: ""}, // Missing token
{ID: "no-port", TunnelPort: 0, RatholeSSHToken: "token"}, // Missing port
}

tunnels := []db.Tunnel{
{ID: "complete", RatholePort: 20000, RatholeToken: "token"},
{ID: "no-port", RatholePort: 0, RatholeToken: "token"}, // Missing port
}

cfg := config.GenerateRatholeServerConfig(machines, tunnels)

if !strings.Contains(cfg, "# gopher-machine-start: complete") {
t.Error("Should include complete machine")
}
if strings.Contains(cfg, "# gopher-machine-start: no-token") {
t.Error("Should skip machine without token")
}
if strings.Contains(cfg, "# gopher-machine-start: no-port") {
t.Error("Should skip machine without port")
}
if !strings.Contains(cfg, "# gopher-tunnel-start: complete") {
t.Error("Should include complete tunnel")
}
if strings.Contains(cfg, "# gopher-tunnel-start: no-port") {
t.Error("Should skip tunnel without port")
}
}

// Test parsing detects unclosed markers
func TestParseUnclosedMarker(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-machine-start: unclosed
[server.services.machine-unclosed-ssh]
token = "token"
bind_addr = "0.0.0.0:10000"
`

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{})

if result.Valid {
t.Error("Should detect unclosed marker")
}
if len(result.Errors) == 0 {
t.Error("Should have parse error about unclosed marker")
}
}

// Test parsing detects marker mismatches
func TestParseMarkerMismatch(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: my-id
[server.services.tunnel-my-id]
token = "token"
bind_addr = "0.0.0.0:20000"
# gopher-tunnel-end: different-id
`

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{})

if result.Valid {
t.Error("Should detect marker mismatch")
}
if len(result.Errors) == 0 {
t.Error("Should have error about mismatched IDs")
}
}

// Test parsing detects orphaned end markers
func TestParseOrphanedEndMarker(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-end: orphaned
`

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{})

if result.Valid {
t.Error("Should detect orphaned end marker")
}
if len(result.Errors) == 0 {
t.Error("Should have error about orphaned marker")
}
}

// Test token mismatch detection
func TestValidateTokenMismatch(t *testing.T) {
cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: test-id
[server.services.tunnel-test-id]
token = "wrong-token"
bind_addr = "0.0.0.0:20000"
# gopher-tunnel-end: test-id
`

tunnels := []db.Tunnel{
{ID: "test-id", RatholePort: 20000, RatholeToken: "correct-token", CreatedAt: time.Now(), UpdatedAt: time.Now()},
}

result := config.ValidateRatholeConfig(cfg, []db.Machine{}, tunnels)

if result.Valid {
t.Error("Should detect token mismatch")
}
hasTokenError := false
for _, err := range result.Errors {
if strings.Contains(err, "Token mismatch") {
hasTokenError = true
break
}
}
if !hasTokenError {
t.Error("Should have token mismatch error")
}
}

// Test port mismatch detection
func TestValidatePortMismatch(t *testing.T) {
	cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: test-id
[server.services.tunnel-test-id]
token = "correct-token"
bind_addr = "0.0.0.0:29999"
# gopher-tunnel-end: test-id
`

	tunnels := []db.Tunnel{
		{ID: "test-id", RatholePort: 20000, RatholeToken: "correct-token", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}

	result := config.ValidateRatholeConfig(cfg, []db.Machine{}, tunnels)

	if result.Valid {
		t.Error("Should detect port mismatch")
	}
	hasPortError := false
	for _, err := range result.Errors {
		if strings.Contains(err, "Port mismatch") {
			hasPortError = true
			break
		}
	}
	if !hasPortError {
		t.Error("Should have port mismatch error")
	}
}

// Test multiple machines generate without duplication
func TestGenerateMultipleMachinesNoDuplicates(t *testing.T) {
	machines := []db.Machine{
		{ID: "mac1", TunnelPort: 10000, RatholeSSHToken: "tok1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "mac2", TunnelPort: 10001, RatholeSSHToken: "tok2", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "mac3", TunnelPort: 10002, RatholeSSHToken: "tok3", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}

	cfg := config.GenerateRatholeServerConfig(machines, []db.Tunnel{})

	// Count start markers
	mac1Count := strings.Count(cfg, "# gopher-machine-start: mac1")
	mac2Count := strings.Count(cfg, "# gopher-machine-start: mac2")
	mac3Count := strings.Count(cfg, "# gopher-machine-start: mac3")

	if mac1Count != 1 {
		t.Errorf("Expected 1 mac1 start marker, got %d", mac1Count)
	}
	if mac2Count != 1 {
		t.Errorf("Expected 1 mac2 start marker, got %d", mac2Count)
	}
	if mac3Count != 1 {
		t.Errorf("Expected 1 mac3 start marker, got %d", mac3Count)
	}

	// Verify all ports present
	if !strings.Contains(cfg, "0.0.0.0:10000") {
		t.Error("Missing port 10000")
	}
	if !strings.Contains(cfg, "0.0.0.0:10001") {
		t.Error("Missing port 10001")
	}
	if !strings.Contains(cfg, "0.0.0.0:10002") {
		t.Error("Missing port 10002")
	}
}

// Test extraction of token and bind_addr from config
func TestParseExtractsTokenAndBindAddr(t *testing.T) {
	cfg := `[server]
bind_addr = "0.0.0.0:2333"

# gopher-tunnel-start: my-tunnel
[server.services.tunnel-my-tunnel]
token = "secret-token-123"
bind_addr = "0.0.0.0:25000"
# gopher-tunnel-end: my-tunnel
`

	result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{})

	// If validation checks can access parsed config directly, verify token/bind_addr were extracted
	// For now, we validate a machine with the same ID and token should pass
	tunnels := []db.Tunnel{
		{ID: "my-tunnel", RatholePort: 25000, RatholeToken: "secret-token-123", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}

	result = config.ValidateRatholeConfig(cfg, []db.Machine{}, tunnels)

	if !result.Valid {
		t.Errorf("Should correctly extract token and bind_addr. Errors: %v", result.Errors)
	}
}

// Test machines appear before tunnels in generated config
func TestGenerateMachinesBeforeTunnels(t *testing.T) {
	machines := []db.Machine{
		{ID: "mac1", TunnelPort: 10000, RatholeSSHToken: "tok1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	tunnels := []db.Tunnel{
		{ID: "tun1", RatholePort: 20000, RatholeToken: "tok1", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}

	cfg := config.GenerateRatholeServerConfig(machines, tunnels)

	machineStartIdx := strings.Index(cfg, "# gopher-machine-start: mac1")
	tunnelStartIdx := strings.Index(cfg, "# gopher-tunnel-start: tun1")

	if machineStartIdx == -1 {
		t.Fatal("Machine marker not found")
	}
	if tunnelStartIdx == -1 {
		t.Fatal("Tunnel marker not found")
	}

	if machineStartIdx > tunnelStartIdx {
		t.Error("Machines should appear before tunnels in config")
	}
}

// Test lifecycle: generate, validate, modify, validate again
func TestConfigLifecycleCycle(t *testing.T) {
	// Step 1: Create initial DB state
	tunnel1 := db.Tunnel{
		ID:           "tunnel-1",
		RatholePort:  20000,
		RatholeToken: "token-1",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Step 2: Generate config
	cfg := config.GenerateRatholeServerConfig([]db.Machine{}, []db.Tunnel{tunnel1})

	// Step 3: Validate - should pass
	result := config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{tunnel1})
	if !result.Valid {
		t.Errorf("Initial validation should pass. Errors: %v", result.Errors)
	}

	// Step 4: Simulate adding second tunnel to DB
	tunnel2 := db.Tunnel{
		ID:           "tunnel-2",
		RatholePort:  20001,
		RatholeToken: "token-2",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	// Step 5: Validate old config against new DB state - should show missing entry
	result = config.ValidateRatholeConfig(cfg, []db.Machine{}, []db.Tunnel{tunnel1, tunnel2})
	if result.Valid {
		t.Error("Should detect missing tunnel-2 in config")
	}
	if len(result.Missing) == 0 {
		t.Error("Should report missing entries")
	}

	// Step 6: Regenerate config from updated DB
	cfgUpdated := config.GenerateRatholeServerConfig([]db.Machine{}, []db.Tunnel{tunnel1, tunnel2})

	// Step 7: Validate new config against new DB - should pass
	result = config.ValidateRatholeConfig(cfgUpdated, []db.Machine{}, []db.Tunnel{tunnel1, tunnel2})
	if !result.Valid {
		t.Errorf("Updated config should validate. Errors: %v", result.Errors)
	}

	// Step 8: Verify both tunnels are in new config
	if !strings.Contains(cfgUpdated, "# gopher-tunnel-start: tunnel-1") {
		t.Error("tunnel-1 should be in updated config")
	}
	if !strings.Contains(cfgUpdated, "# gopher-tunnel-start: tunnel-2") {
		t.Error("tunnel-2 should be in updated config")
	}
}

// Test config drift - detect when manual modifications made
func TestConfigDriftDetection(t *testing.T) {
	// Create valid config
	tunnel := db.Tunnel{
		ID:           "my-tunnel",
		RatholePort:  20000,
		RatholeToken: "my-token",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	cfg := config.GenerateRatholeServerConfig([]db.Machine{}, []db.Tunnel{tunnel})

	// Corrupt the config: change token manually
	corruptedCfg := strings.ReplaceAll(cfg, "my-token", "hacked-token")

	// Validate corrupted config against original DB - should detect mismatch
	result := config.ValidateRatholeConfig(corruptedCfg, []db.Machine{}, []db.Tunnel{tunnel})

	if result.Valid {
		t.Error("Should detect config corruption (token mismatch)")
	}

	hasTokenMismatch := false
	for _, err := range result.Errors {
		if strings.Contains(err, "Token mismatch") {
			hasTokenMismatch = true
			break
		}
	}
	if !hasTokenMismatch {
		t.Error("Should report token mismatch")
	}
}

// Test empty database generates valid structure
func TestGenerateEmptyDatabaseValidStructure(t *testing.T) {
	cfg := config.GenerateRatholeServerConfig([]db.Machine{}, []db.Tunnel{})

	// Should have server section
	if !strings.Contains(cfg, "[server]") {
		t.Error("Should have [server] section")
	}

	// Should have bind address
	if !strings.Contains(cfg, "bind_addr = \"0.0.0.0:2333\"") {
		t.Error("Should have default bind_addr")
	}

	// Should not have machine markers
	if strings.Contains(cfg, "# gopher-machine-start") {
		t.Error("Should not have machine markers when empty")
	}

	// Should not have tunnel markers
	if strings.Contains(cfg, "# gopher-tunnel-start") {
		t.Error("Should not have tunnel markers when empty")
	}
}

// Test placeholder is generated when all DB entries are incomplete/skipped
func TestGenerateAddsPlaceholderWhenAllEntriesIncomplete(t *testing.T) {
	machines := []db.Machine{
		{ID: "no-token", TunnelPort: 10000, RatholeSSHToken: ""},
		{ID: "no-port", TunnelPort: 0, RatholeSSHToken: "ssh-token"},
	}
	tunnels := []db.Tunnel{
		{ID: "no-port", RatholePort: 0, RatholeToken: "tok"},
	}

	cfg := config.GenerateRatholeServerConfig(machines, tunnels)

	if !strings.Contains(cfg, "[server.services.placeholder]") {
		t.Error("Should include placeholder when all entries are skipped")
	}
	if !strings.Contains(cfg, "bind_addr = \"0.0.0.0:52000\"") {
		t.Error("Placeholder bind_addr should be present")
	}
}