package service

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
	"github.com/smalex-z/gopher/internal/paths"
)

// The path aliases follow paths' vars (test-redirectable), so they are vars.
var (
	caddyConfigPath = paths.CaddyfilePath
	caddyManagedDir = paths.CaddyConfDir
)

const (
	caddyCustomBeginMark = "# ===== BEGIN CUSTOM CONFIGURATION ====="
	caddyCustomEndMark   = "# ===== END CUSTOM CONFIGURATION ====="
)

func managedRouterCaddyPath() string {
	return caddyManagedDir + "/gopher-router.caddy"
}

func managedTunnelCaddyPath(tunnelID string) string {
	return fmt.Sprintf("%s/gopher-tunnel-%s.caddy", caddyManagedDir, tunnelID)
}

func buildRouterCaddyBlock(domain, bindIP string) string {
	return fmt.Sprintf("router.%s {\n    reverse_proxy localhost:%d\n}\n", domain, dashboardPort)
}

// caddyAvailable reports whether there's a Caddy we can manage: the bundled
// binary under /opt/gopher/bin on an embedded/supervised install (which is NOT
// on PATH), or a caddy on PATH for a dev/manual setup. A bare
// isCommandAvailable("caddy") misses the supervised binary and makes the
// reconciles below silently no-op on a clean embedded edge. Mirrors caddyReload's
// own bundled-then-PATH precedence.
func caddyAvailable() bool {
	if _, err := os.Stat(paths.CaddyBin); err == nil {
		return true
	}
	return isCommandAvailable("caddy")
}

// ReconcileRouterCaddyBlock rewrites the managed router Caddy file to reflect
// the current dashboardPort and bindIP. Called at startup so binary updates that
// change the default port don't leave a stale Caddy config pointing at the old port.
func (s *LocalSetupService) ReconcileRouterCaddyBlock() {
	if !caddyAvailable() {
		return
	}
	settings, err := db.GetSettings()
	if err != nil || settings.Domain == "" || !settings.LocalSetupDone {
		return
	}
	if err := writeLocalFile(managedRouterCaddyPath(), buildRouterCaddyBlock(settings.Domain, settings.BindIP)); err != nil {
		log.Printf("startup: failed to reconcile router Caddy block: %v", err)
		return
	}
	if err := caddyReload(); err != nil {
		log.Printf("startup: caddy reload failed: %v", err)
	}
}

func buildTunnelCaddyBlock(subdomain, domain string, ratholePort int, noTLS bool, proxied bool, bindIP string, tlsSkipVerify bool, private bool) string {
	scheme := ""
	if noTLS {
		scheme = "http://"
	}
	// Gated tunnels (bot protection and/or password auth) route through the
	// Gopher server itself (same port as the dashboard) so the gate middleware
	// can intercept requests before they reach rathole. Host header routing
	// distinguishes tunnel traffic from dashboard traffic.
	// Public tunnel rathole ports bind to bind_ip (or 0.0.0.0), so Caddy proxies
	// to bind_ip:ratholePort. Gated tunnels route to Gopher itself which is on
	// 127.0.0.1, so those always use localhost regardless of bind_ip.
	upstreamPort := ratholePort
	upstream := "localhost"
	// Public tunnels bind to bind_ip; private tunnels bind to 127.0.0.1 (only
	// Caddy reaches them), so private must proxy via localhost regardless of
	// bind_ip — otherwise Caddy would proxy to an address the tunnel isn't on.
	if bindIP != "" && !private {
		upstream = bindIP
	}
	if proxied {
		upstreamPort = dashboardPort
		upstream = "localhost"
	}
	// TLS skip verify: only meaningful when the upstream is itself HTTPS (noTLS=false,
	// not routed through Gopher) and the backend uses a self-signed cert (e.g. Proxmox).
	if tlsSkipVerify && !noTLS && !proxied {
		return fmt.Sprintf("%s%s.%s {\n    reverse_proxy %s:%d {\n        transport http {\n            tls_insecure_skip_verify\n        }\n    }\n}\n",
			scheme, subdomain, domain, upstream, upstreamPort)
	}
	return fmt.Sprintf("%s%s.%s {\n    reverse_proxy %s:%d\n}\n", scheme, subdomain, domain, upstream, upstreamPort)
}

// caddyCustomHeaderLines are the boilerplate comment lines we emit inside
// the custom-section markers. Listed here so extractCaddyCustomBody can
// strip them when re-reading existing files — without that, every rewrite
// of the Caddyfile would re-add the header lines and never remove the old
// ones, accumulating dozens of "Everything below this line..." comments
// over many reconciles.
var caddyCustomHeaderLines = []string{
	"# Everything below this line will NOT be overwritten.",
	"# Add your own Caddy site blocks here.",
}

// managedCaddyCommentLines are Gopher-emitted comment lines that must be
// stripped from any extracted custom body — a superset of caddyCustomHeaderLines
// that also includes the top-of-file managed header. The managed header lives
// OUTSIDE the markers when we write it, but a legacy/whole-file absorb can scoop
// it into the custom section, and once there it's sticky: every reconsruct
// re-wraps it, stacking copies. We never write these into the custom block, so
// dropping every occurrence is always safe and self-heals old accumulation.
var managedCaddyCommentLines = append([]string{
	"# Gopher managed Caddyfile",
}, caddyCustomHeaderLines...)

// managedCaddyHeaderBlock is the commented global-options block emitted below
// the managed header (the bindIP=="" branch of buildManagedCaddyfile). It is
// stripped as a contiguous sequence, not line by line, because "# {" / "# }"
// on their own are too generic to remove from user content safely.
var managedCaddyHeaderBlock = []string{
	"# Global options (uncomment and set email to enable HTTPS):",
	"# {",
	"#     email you@example.com",
	"# }",
}

// ExtractUserCaddyConfig returns the operator's own Caddy configuration from a
// Gopher Caddyfile: the content between the custom-config markers with all
// Gopher boilerplate stripped (managed header, "add your own blocks" comments,
// the managed conf.d import). A file with no markers was never Gopher-wrapped,
// so the whole thing is the user's and returned as-is. Shared with the uninstall
// flow so "reset" leaves exactly the user's config and nothing of Gopher's.
func ExtractUserCaddyConfig(content string) string {
	if !strings.Contains(content, caddyCustomBeginMark) {
		return content
	}
	body := extractCaddyCustomBody(content)
	body = strings.TrimSpace(stripManagedCaddyImports(body))
	body = stripManagedCaddyComments(body)
	if body == "" {
		return ""
	}
	return body + "\n"
}

// stripManagedCaddyComments drops every line that exactly matches a Gopher
// managed comment, anywhere in the body — clears stray/stacked headers that a
// leading-only strip would miss. Also removes every occurrence of the
// commented global-options block as a sequence.
func stripManagedCaddyComments(body string) string {
	managed := make(map[string]struct{}, len(managedCaddyCommentLines))
	for _, l := range managedCaddyCommentLines {
		managed[l] = struct{}{}
	}
	lines := stripManagedCaddyHeaderBlocks(strings.Split(body, "\n"))
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if _, ok := managed[strings.TrimSpace(line)]; ok {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// stripManagedCaddyHeaderBlocks removes every contiguous occurrence of
// managedCaddyHeaderBlock (whitespace-trimmed comparison per line).
func stripManagedCaddyHeaderBlocks(lines []string) []string {
	matchesAt := func(i int) bool {
		if i+len(managedCaddyHeaderBlock) > len(lines) {
			return false
		}
		for j, want := range managedCaddyHeaderBlock {
			if strings.TrimSpace(lines[i+j]) != want {
				return false
			}
		}
		return true
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		if matchesAt(i) {
			i += len(managedCaddyHeaderBlock)
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return out
}

func extractCaddyCustomBody(content string) string {
	bIdx := strings.Index(content, caddyCustomBeginMark)
	if bIdx == -1 {
		return ""
	}
	below := content[bIdx+len(caddyCustomBeginMark):]
	eIdx := strings.Index(below, caddyCustomEndMark)
	var inner string
	if eIdx == -1 {
		inner = below
	} else {
		inner = below[:eIdx]
	}
	return stripCaddyCustomHeader(inner)
}

// stripCaddyCustomHeader removes any leading occurrences of the boilerplate
// comment lines we emit inside the custom-section markers. Tolerates blank
// lines between repeated header copies (the prior bug accumulated stacks of
// header lines, and we want to fully clean those up on the next reconcile).
func stripCaddyCustomHeader(s string) string {
	lines := strings.Split(s, "\n")
	i := 0
	headerSet := make(map[string]struct{}, len(managedCaddyCommentLines))
	for _, h := range managedCaddyCommentLines {
		headerSet[h] = struct{}{}
	}
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			i++
			continue
		}
		if _, ok := headerSet[t]; ok {
			i++
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[i:], "\n"))
}

// stripManagedCaddyImports removes any `import .../conf.d/*.caddy` line from
// custom Caddy content. Gopher manages the import directive itself; a stale one
// absorbed from a legacy Caddyfile would import the wrong conf.d.
func stripManagedCaddyImports(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "import ") && strings.Contains(t, "conf.d") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func buildManagedCaddyfile(existing, bindIP string) string {
	customBody := extractCaddyCustomBody(existing)
	if customBody == "" && strings.TrimSpace(existing) != "" && !strings.Contains(existing, caddyCustomBeginMark) {
		// Legacy file with no markers — treat the whole thing as user content.
		// Don't fall back to whole-file content when markers are present:
		// extractCaddyCustomBody already returned "" because the section was
		// genuinely empty, and falling back here would scoop the entire
		// generated header back into the custom section.
		customBody = strings.TrimSpace(existing)
	}
	// A legacy/non-gopher Caddyfile (e.g. the apt default, or a box that already
	// served its own site) carries its own `import .../conf.d/*.caddy` line.
	// Absorbed into the custom section it would re-import a stale conf.d (e.g. an
	// old /etc/caddy/conf.d/gopher-router.caddy), producing "ambiguous site
	// definition" errors that crash-loop caddy. Gopher owns the import directive
	// (re-added below), so strip any conf.d import from the custom body.
	// Managed comment boilerplate gets the same treatment: an absorbed
	// previously-reset Caddyfile (or old stacked-header accumulation) would
	// otherwise re-wrap gopher's own headers as "user content" on every
	// reconcile, sticky forever.
	customBody = stripManagedCaddyComments(stripManagedCaddyImports(customBody))

	var out strings.Builder
	out.WriteString("# Gopher managed Caddyfile\n")
	if bindIP != "" {
		out.WriteString("{\n")
		out.WriteString(fmt.Sprintf("    default_bind %s\n", bindIP))
		out.WriteString("    # Uncomment and set email to enable HTTPS:\n")
		out.WriteString("    # email you@example.com\n")
		out.WriteString("}\n\n")
	} else {
		out.WriteString("# Global options (uncomment and set email to enable HTTPS):\n")
		out.WriteString("# {\n")
		out.WriteString("#     email you@example.com\n")
		out.WriteString("# }\n\n")
	}
	out.WriteString("import " + paths.CaddyConfDir + "/*.caddy\n\n")
	out.WriteString(caddyCustomBeginMark + "\n")
	for _, h := range caddyCustomHeaderLines {
		out.WriteString(h + "\n")
	}
	if customBody != "" {
		out.WriteString(customBody + "\n")
	}
	out.WriteString(caddyCustomEndMark + "\n")
	return out.String()
}

func ensureManagedCaddyLayout() error {
	if err := sudoMkdir(caddyManagedDir); err != nil {
		return err
	}
	existing := ""
	if data, err := os.ReadFile(caddyConfigPath); err == nil {
		existing = string(data)
	}
	bindIP := ""
	if settings, err := db.GetSettings(); err == nil {
		bindIP = settings.BindIP
	}
	return writeLocalFile(caddyConfigPath, buildManagedCaddyfile(existing, bindIP))
}

// ReconcileMainCaddyfile rewrites the main Caddyfile global options (e.g.
// default_bind) to match current settings. Called when bind_ip changes.
func (s *LocalSetupService) ReconcileMainCaddyfile() {
	if !caddyAvailable() {
		return
	}
	if err := ensureManagedCaddyLayout(); err != nil {
		log.Printf("reconcile main Caddyfile: %v", err)
		return
	}
	if err := caddyReload(); err != nil {
		log.Printf("reconcile main Caddyfile: caddy reload failed: %v", err)
	}
}

// ReconcileTunnelCaddyFiles drops conf.d/gopher-tunnel-<id>.caddy files whose
// tunnel ID is no longer in the DB. Without this sweep, tunnels deleted while
// the dashboard was offline (or whose RemoveServiceTunnelCaddy call failed
// silently in older versions) leave their per-subdomain Caddy block on disk
// forever — and a subsequent tunnel reusing the same subdomain produces the
// "ambiguous site definition" error that blocks every Caddy reload.
//
// Called once at startup. Idempotent — files belonging to live tunnels are
// untouched, the gopher-router.caddy is preserved (its filename has no tunnel
// ID), and an empty conf.d directory is a no-op.
func (s *LocalSetupService) ReconcileTunnelCaddyFiles() {
	if !caddyAvailable() {
		return
	}
	entries, err := os.ReadDir(caddyManagedDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("reconcile tunnel caddy files: read %s: %v", caddyManagedDir, err)
		}
		return
	}

	tunnels, err := db.GetTunnels()
	if err != nil {
		log.Printf("reconcile tunnel caddy files: load tunnels: %v", err)
		return
	}
	live := make(map[string]struct{}, len(tunnels))
	for _, t := range tunnels {
		live[t.ID] = struct{}{}
	}

	const prefix = "gopher-tunnel-"
	const suffix = ".caddy"
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
		if _, ok := live[id]; ok {
			continue
		}
		path := caddyManagedDir + "/" + name
		if err := os.Remove(path); err != nil {
			if rerr := runSudoCommand("rm", "-f", path); rerr != nil {
				log.Printf("reconcile tunnel caddy files: remove %s: %v / sudo: %v", path, err, rerr)
				continue
			}
		}
		log.Printf("reconcile tunnel caddy files: removed orphan %s", name)
		removed++
	}
	if removed > 0 {
		if err := caddyReload(); err != nil {
			log.Printf("reconcile tunnel caddy files: caddy reload failed: %v", err)
		}
	}
}

// removeCaddyBlock removes a top-level site block that starts with "host {".
func removeCaddyBlock(content, host string) string {
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	skip := false
	depth := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !skip && strings.HasPrefix(trimmed, host) && strings.HasSuffix(trimmed, "{") {
			skip = true
			depth = 1
			continue
		}
		if skip {
			for _, ch := range line {
				if ch == '{' {
					depth++
				} else if ch == '}' {
					depth--
				}
			}
			if depth <= 0 {
				skip = false
			}
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func removeHostsFromCustomSection(content string, hosts []string) string {
	bIdx := strings.Index(content, caddyCustomBeginMark)
	if bIdx == -1 {
		return content
	}
	below := content[bIdx+len(caddyCustomBeginMark):]
	eIdx := strings.Index(below, caddyCustomEndMark)
	if eIdx == -1 {
		return content
	}

	customBody := below[:eIdx]
	for _, host := range hosts {
		if host == "" {
			continue
		}
		customBody = removeCaddyBlock(customBody, host)
	}
	customBody = strings.TrimSpace(customBody)

	var rebuilt strings.Builder
	rebuilt.WriteString(content[:bIdx+len(caddyCustomBeginMark)])
	rebuilt.WriteString("\n")
	rebuilt.WriteString("# Everything below this line will NOT be overwritten.\n")
	rebuilt.WriteString("# Add your own Caddy site blocks here.\n")
	if customBody != "" {
		rebuilt.WriteString(customBody)
		rebuilt.WriteString("\n")
	}
	rebuilt.WriteString(caddyCustomEndMark)
	rebuilt.WriteString(below[eIdx+len(caddyCustomEndMark):])
	return rebuilt.String()
}
