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
