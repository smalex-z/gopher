package service

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/smalex-z/gopher/internal/db"
)

const (
	caddyConfigPath      = "/etc/caddy/Caddyfile"
	caddyManagedDir      = "/etc/caddy/conf.d"
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

// ReconcileRouterCaddyBlock rewrites the managed router Caddy file to reflect
// the current dashboardPort and bindIP. Called at startup so binary updates that
// change the default port don't leave a stale Caddy config pointing at the old port.
func (s *LocalSetupService) ReconcileRouterCaddyBlock() {
	if !isCommandAvailable("caddy") {
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
	if err := systemctlReloadOrRestart("caddy"); err != nil {
		log.Printf("startup: caddy reload-or-restart failed: %v", err)
	}
}

func buildTunnelCaddyBlock(subdomain, domain string, ratholePort int, noTLS bool, botProtected bool, bindIP string, tlsSkipVerify bool) string {
	scheme := ""
	if noTLS {
		scheme = "http://"
	}
	// Bot-protected tunnels route through the Gopher server itself (same port
	// as the dashboard) so the bot-protection middleware can intercept requests
	// before they reach rathole. Host header routing distinguishes tunnel
	// traffic from dashboard traffic.
	// Public tunnel rathole ports bind to bind_ip (or 0.0.0.0), so Caddy proxies
	// to bind_ip:ratholePort. Bot-protected tunnels route to Gopher itself which
	// is on 127.0.0.1, so those always use localhost regardless of bind_ip.
	upstreamPort := ratholePort
	upstream := "localhost"
	if bindIP != "" {
		upstream = bindIP
	}
	if botProtected {
		upstreamPort = dashboardPort
		upstream = "localhost"
	}
	// TLS skip verify: only meaningful when the upstream is itself HTTPS (noTLS=false,
	// botProtected=false) and the backend uses a self-signed cert (e.g. Proxmox).
	if tlsSkipVerify && !noTLS && !botProtected {
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
	headerSet := make(map[string]struct{}, len(caddyCustomHeaderLines))
	for _, h := range caddyCustomHeaderLines {
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
	out.WriteString("import /etc/caddy/conf.d/*.caddy\n\n")
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
	if !isCommandAvailable("caddy") {
		return
	}
	if err := ensureManagedCaddyLayout(); err != nil {
		log.Printf("reconcile main Caddyfile: %v", err)
		return
	}
	if err := systemctlReloadOrRestart("caddy"); err != nil {
		log.Printf("reconcile main Caddyfile: caddy reload-or-restart failed: %v", err)
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
	if !isCommandAvailable("caddy") {
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
		if err := systemctlReloadOrRestart("caddy"); err != nil {
			log.Printf("reconcile tunnel caddy files: caddy reload-or-restart failed: %v", err)
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
