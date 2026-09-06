package service

import (
	"os"
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/paths"
)

// Regression for the ACM-edge incident: ReconcileServerConfig only recognised
// the current literal marker string, so a server.toml written by an older
// gopher version — same custom-section concept, older wording — looked like
// it had NO custom section at all, and a hand-added service (e.g. a Proxmox
// VNC tunnel) would be silently dropped on the very next reconcile.
// extractCustomBody must fall back through legacyCustomMarkers instead of
// returning "" the moment the current marker isn't found verbatim.

func TestExtractCustomBody_CurrentMarker(t *testing.T) {
	content := `[server]
bind_addr = "0.0.0.0:2333"

# ===== BEGIN CUSTOM CONFIGURATION =====
[server.services.my_service]
token = "abc"
bind_addr = "0.0.0.0:9000"
# ===== END CUSTOM CONFIGURATION =====
`
	got := extractCustomBody(content, "# ===== BEGIN CUSTOM CONFIGURATION =====", "# ===== END CUSTOM CONFIGURATION =====")
	if !strings.Contains(got, "server.services.my_service") {
		t.Fatalf("expected custom service preserved, got:\n%s", got)
	}
}

func TestExtractCustomBody_LegacyMarkerFallsBack(t *testing.T) {
	// Verbatim shape of the real ACM server.toml that triggered this bug.
	content := `[server]
bind_addr = "0.0.0.0:2333"

[server.services.machine-abc-ssh]
token = "tok1"
bind_addr = "0.0.0.0:1052"

# Add your own rathole service entries here. Gopher will not modify this section.
#Proxmox for ACM
[server.services.acm_proxmox]
token = "devteam#1"
bind_addr = "0.0.0.0:5900"
`
	got := extractCustomBody(content, "# ===== BEGIN CUSTOM CONFIGURATION =====", "# ===== END CUSTOM CONFIGURATION =====")
	if !strings.Contains(got, "server.services.acm_proxmox") || !strings.Contains(got, `bind_addr = "0.0.0.0:5900"`) {
		t.Fatalf("legacy-marker custom content must survive, got:\n%s", got)
	}
	// The gopher-managed SSH block sits ABOVE the legacy marker in this file,
	// so it's never part of "below" in the first place — but assert it isn't
	// duplicated into the custom body regardless, since a duplicate would
	// produce a token/port conflict on the very next write.
	if strings.Contains(got, "machine-abc-ssh") {
		t.Fatalf("gopher-managed block leaked into extracted custom body:\n%s", got)
	}
}

func TestExtractCustomBody_NoMarkerAtAllReturnsEmpty(t *testing.T) {
	// A file with genuinely no custom section (current or legacy) must still
	// return "" — we only special-case wording drift, never invent content.
	content := `[server]
bind_addr = "0.0.0.0:2333"

[server.services.machine-abc-ssh]
token = "tok1"
bind_addr = "0.0.0.0:1052"
`
	if got := extractCustomBody(content, "# ===== BEGIN CUSTOM CONFIGURATION =====", "# ===== END CUSTOM CONFIGURATION ====="); got != "" {
		t.Fatalf("expected empty custom body with no marker present, got:\n%s", got)
	}
}

func TestReconcileServerConfig_SelfHealsLegacyMarkerAndKeepsCustomEntry(t *testing.T) {
	initTestDB(t)
	cfgPath := t.TempDir() + "/server.toml"

	legacy := `[server]
bind_addr = "0.0.0.0:2333"

# Add your own rathole service entries here. Gopher will not modify this section.
#Proxmox for ACM
[server.services.acm_proxmox]
token = "devteam#1"
bind_addr = "0.0.0.0:5900"
`
	if err := os.WriteFile(cfgPath, []byte(legacy), 0644); err != nil {
		t.Fatalf("seed legacy config: %v", err)
	}

	orig := paths.RatholeConfig
	paths.RatholeConfig = cfgPath
	t.Cleanup(func() { paths.RatholeConfig = orig })

	svc := &LocalSetupService{}
	if err := svc.ReconcileServerConfig(); err != nil {
		t.Fatalf("ReconcileServerConfig: %v", err)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read reconciled config: %v", err)
	}
	if !strings.Contains(string(got), "# ===== BEGIN CUSTOM CONFIGURATION =====") {
		t.Errorf("expected the file to be self-healed onto the current marker, got:\n%s", got)
	}
	if !strings.Contains(string(got), "server.services.acm_proxmox") || !strings.Contains(string(got), `bind_addr = "0.0.0.0:5900"`) {
		t.Errorf("custom Proxmox entry must survive the reconcile, got:\n%s", got)
	}

	// Re-run: the file is now under the current marker, so a second pass
	// must be a stable no-op for the custom content (not lost, not doubled).
	if err := svc.ReconcileServerConfig(); err != nil {
		t.Fatalf("second ReconcileServerConfig: %v", err)
	}
	got2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read twice-reconciled config: %v", err)
	}
	if strings.Count(string(got2), "server.services.acm_proxmox") != 1 {
		t.Errorf("custom entry must appear exactly once after a second reconcile, got:\n%s", got2)
	}
}
