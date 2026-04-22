package service

import (
	"strings"
	"testing"
)

func TestBuildManagedCaddyfile_UsesImportLayout(t *testing.T) {
	existing := `{
    email admin@example.com
}

router.example.com {
    reverse_proxy localhost:8080
}

# ===== BEGIN CUSTOM CONFIGURATION =====
custom.example.com {
    reverse_proxy localhost:9000
}
# ===== END CUSTOM CONFIGURATION =====
`

	content := buildManagedCaddyfile(existing, "")
	if !strings.Contains(content, "import /etc/caddy/conf.d/*.caddy") {
		t.Fatalf("expected import-based layout, got:\n%s", content)
	}
	if strings.Contains(content, "router.example.com {") {
		t.Fatalf("router block should not live in top-level Caddyfile after migration, got:\n%s", content)
	}
	if !strings.Contains(content, "custom.example.com {") {
		t.Fatalf("expected custom section to be preserved, got:\n%s", content)
	}
}

func TestBuildManagedCaddyfile_MigratesLegacyFileIntoCustomSection(t *testing.T) {
	existing := `:80 {
    file_server
}

photos.example.com {
    reverse_proxy localhost:20005
}
`
	content := buildManagedCaddyfile(existing, "")

	if !strings.Contains(content, "import /etc/caddy/conf.d/*.caddy") {
		t.Fatalf("expected import-based layout, got:\n%s", content)
	}
	if !strings.Contains(content, "# ===== BEGIN CUSTOM CONFIGURATION =====") || !strings.Contains(content, "# ===== END CUSTOM CONFIGURATION =====") {
		t.Fatalf("expected custom markers in migrated file, got:\n%s", content)
	}
	if !strings.Contains(content, ":80 {") || !strings.Contains(content, "photos.example.com {") {
		t.Fatalf("expected legacy content preserved inside custom section, got:\n%s", content)
	}
}

func TestManagedCaddyPaths(t *testing.T) {
	if got := managedRouterCaddyPath(); got != "/etc/caddy/conf.d/gopher-router.caddy" {
		t.Fatalf("unexpected router path: %s", got)
	}
	if got := managedTunnelCaddyPath("abc123"); got != "/etc/caddy/conf.d/gopher-tunnel-abc123.caddy" {
		t.Fatalf("unexpected tunnel path: %s", got)
	}
}
