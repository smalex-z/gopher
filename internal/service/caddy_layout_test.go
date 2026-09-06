package service

import (
	"strings"
	"testing"

	"github.com/smalex-z/gopher/internal/paths"
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
	if !strings.Contains(content, "import "+paths.CaddyConfDir+"/*.caddy") {
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

	if !strings.Contains(content, "import "+paths.CaddyConfDir+"/*.caddy") {
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
	if got, want := managedRouterCaddyPath(), paths.CaddyConfDir+"/gopher-router.caddy"; got != want {
		t.Fatalf("router path = %s, want %s", got, want)
	}
	if got, want := managedTunnelCaddyPath("abc123"), paths.CaddyConfDir+"/gopher-tunnel-abc123.caddy"; got != want {
		t.Fatalf("tunnel path = %s, want %s", got, want)
	}
}

// Regression for the gopherden migration: a legacy/non-gopher Caddyfile carrying
// its own `import .../conf.d/*.caddy` line must not have that import survive in
// the custom section — else it re-imports a stale conf.d and caddy crash-loops
// with "ambiguous site definition".
func TestBuildManagedCaddyfile_StripsLegacyConfDImport(t *testing.T) {
	existing := "import /etc/caddy/conf.d/*.caddy\n\n" +
		"gopherden.org {\n\troot * /var/www/gopherden\n\tfile_server\n}\n"
	out := buildManagedCaddyfile(existing, "")

	if !strings.Contains(out, "import "+paths.CaddyConfDir+"/*.caddy") {
		t.Fatalf("expected the managed conf.d import, got:\n%s", out)
	}
	if strings.Contains(out, "import /etc/caddy/conf.d") {
		t.Fatalf("legacy conf.d import leaked into the custom section:\n%s", out)
	}
	if !strings.Contains(out, "gopherden.org {") {
		t.Fatalf("user's custom site block was dropped:\n%s", out)
	}
}

// Regression for the uninstall leftover: extracting the operator's config must
// return ONLY their site blocks — no managed header, boilerplate comments,
// conf.d import, or stray/stacked "# Gopher managed Caddyfile" lines an old
// reconcile may have accumulated inside the custom section.
func TestExtractUserCaddyConfig_StripsAllGopherBoilerplate(t *testing.T) {
	dirty := "# Gopher managed Caddyfile\n" +
		"import " + paths.CaddyConfDir + "/*.caddy\n\n" +
		caddyCustomBeginMark + "\n" +
		"# Gopher managed Caddyfile\n" + // stray, stacked
		"# Gopher managed Caddyfile\n" +
		"# Global options (uncomment and set email to enable HTTPS):\n" +
		"# {\n" +
		"#     email you@example.com\n" +
		"# }\n" +
		"# Everything below this line will NOT be overwritten.\n" +
		"# Add your own Caddy site blocks here.\n" +
		"import /etc/caddy/conf.d/*.caddy\n" +
		"gopherden.org {\n\troot * /var/www/gopherden\n\tfile_server\n}\n" +
		caddyCustomEndMark + "\n"

	got := ExtractUserCaddyConfig(dirty)

	if strings.Contains(got, "# Gopher managed Caddyfile") {
		t.Fatalf("stray managed header leaked into user config:\n%s", got)
	}
	if strings.Contains(got, "# Global options") || strings.Contains(got, "email you@example.com") {
		t.Fatalf("global-options boilerplate leaked into user config:\n%s", got)
	}
	if strings.Contains(got, "Everything below this line") || strings.Contains(got, "Add your own Caddy") {
		t.Fatalf("boilerplate comments leaked:\n%s", got)
	}
	if strings.Contains(got, "import ") {
		t.Fatalf("conf.d import leaked:\n%s", got)
	}
	if !strings.Contains(got, "gopherden.org {") || !strings.Contains(got, "file_server") {
		t.Fatalf("user's site block was dropped:\n%s", got)
	}
}

// Regression for the beta.15→16 upgrade leftover: an old uninstall reset the
// Caddyfile but left stacked managed headers + global-options comment blocks
// in it; the next install absorbed that whole file as "user content". The
// reconcile path must self-heal — strip every copy of gopher's boilerplate
// while keeping the operator's real site blocks.
func TestBuildManagedCaddyfile_SelfHealsAbsorbedBoilerplate(t *testing.T) {
	// Marker-less file shaped like the real-world leftover: two stacked
	// header+global-options copies, then the operator's sites.
	headerCopy := "# Gopher managed Caddyfile\n" +
		"# Global options (uncomment and set email to enable HTTPS):\n" +
		"# {\n" +
		"#     email you@example.com\n" +
		"# }\n\n"
	existing := headerCopy + headerCopy +
		"gopherden.org {\n\troot * /var/www/gopherden\n\tfile_server\n}\n"

	out := buildManagedCaddyfile(existing, "")

	// Exactly one of each: the freshly-emitted top-of-file copy — none inside
	// the custom section.
	if got := strings.Count(out, "# Gopher managed Caddyfile"); got != 1 {
		t.Fatalf("expected exactly 1 managed header, got %d:\n%s", got, out)
	}
	if got := strings.Count(out, "# Global options (uncomment and set email to enable HTTPS):"); got != 1 {
		t.Fatalf("expected exactly 1 global-options block, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "gopherden.org {") {
		t.Fatalf("user's site block was dropped:\n%s", out)
	}
	// Idempotence: re-reconciling the healed output must not grow it.
	again := buildManagedCaddyfile(out, "")
	if again != out {
		t.Fatalf("reconcile not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, again)
	}
}

// The commented global-options block is stripped only as a contiguous
// sequence — a lone "# {" or "# }" in user content must survive.
func TestStripManagedCaddyComments_KeepsLoneBraceComments(t *testing.T) {
	body := "# {\n" +
		"# my own commented-out block\n" +
		"# }\n" +
		"example.com {\n\treverse_proxy localhost:8080\n}\n"
	got := stripManagedCaddyComments(body)
	if !strings.Contains(got, "# {") || !strings.Contains(got, "# }") {
		t.Fatalf("lone commented braces were eaten:\n%s", got)
	}
}

// A Caddyfile with no custom markers was never Gopher-wrapped (operator's own
// pre-existing config) — it must come back verbatim, not get blanked.
func TestExtractUserCaddyConfig_NoMarkersReturnedVerbatim(t *testing.T) {
	own := "example.com {\n\treverse_proxy localhost:8080\n}\n"
	if got := ExtractUserCaddyConfig(own); got != own {
		t.Fatalf("non-gopher Caddyfile altered:\n--- in ---\n%s\n--- out ---\n%s", own, got)
	}
}

// An all-Gopher Caddyfile (no operator config) extracts to empty, so uninstall
// removes the file instead of leaving an empty Gopher-owned one.
func TestExtractUserCaddyConfig_GopherOnlyIsEmpty(t *testing.T) {
	gopherOnly := buildManagedCaddyfile("", "")
	if got := strings.TrimSpace(ExtractUserCaddyConfig(gopherOnly)); got != "" {
		t.Fatalf("expected empty extraction for gopher-only file, got:\n%s", got)
	}
}
