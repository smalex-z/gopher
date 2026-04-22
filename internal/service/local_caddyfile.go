package service

import (
	"fmt"
	"log"
	"os"
	"os/exec"
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
	bind := ""
	if bindIP != "" {
		bind = fmt.Sprintf("    bind %s\n", bindIP)
	}
	return fmt.Sprintf("router.%s {\n%s    reverse_proxy localhost:%d\n}\n", domain, bind, dashboardPort)
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
	_ = exec.Command("sudo", "systemctl", "reload-or-restart", "caddy").Run() // #nosec G204
}

func buildTunnelCaddyBlock(subdomain, domain string, ratholePort int, noTLS bool, botProtected bool, bindIP string) string {
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
	bind := ""
	if bindIP != "" {
		bind = fmt.Sprintf("    bind %s\n", bindIP)
	}
	return fmt.Sprintf("%s%s.%s {\n%s    reverse_proxy %s:%d\n}\n", scheme, subdomain, domain, bind, upstream, upstreamPort)
}

func extractCaddyCustomBody(content string) string {
	bIdx := strings.Index(content, caddyCustomBeginMark)
	if bIdx == -1 {
		return ""
	}
	below := content[bIdx+len(caddyCustomBeginMark):]
	eIdx := strings.Index(below, caddyCustomEndMark)
	if eIdx == -1 {
		return strings.TrimSpace(below)
	}
	return strings.TrimSpace(below[:eIdx])
}

func buildManagedCaddyfile(existing string) string {
	customBody := extractCaddyCustomBody(existing)
	if customBody == "" && strings.TrimSpace(existing) != "" {
		customBody = strings.TrimSpace(existing)
	}

	var out strings.Builder
	out.WriteString("# Gopher managed Caddyfile\n")
	out.WriteString("# Global options (uncomment and set email to enable HTTPS):\n")
	out.WriteString("# {\n")
	out.WriteString("#     email you@example.com\n")
	out.WriteString("# }\n\n")
	out.WriteString("import /etc/caddy/conf.d/*.caddy\n\n")
	out.WriteString(caddyCustomBeginMark + "\n")
	out.WriteString("# Everything below this line will NOT be overwritten.\n")
	out.WriteString("# Add your own Caddy site blocks here.\n")
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
	return writeLocalFile(caddyConfigPath, buildManagedCaddyfile(existing))
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
