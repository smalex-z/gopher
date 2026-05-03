package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/smalex-z/gopher/internal/db"
)

//go:embed templates/rathole-client.toml.tmpl
var ratholeClientTemplate string

// GopherEntry represents a marker-delimited config entry
type GopherEntry struct {
	Type     string
	ID       string
	Token    string
	BindAddr string
}

// ValidationResult is the output of config validation
type ValidationResult struct {
	Valid      bool
	Errors     []string
	Duplicates []string
	Orphans    []string
	Missing    []string
}

type clientData struct {
	VPSHost string
	Tunnels []db.Tunnel
}

func GenerateClientConfig(vpsHost string, tunnels []db.Tunnel) (string, error) {
	tmpl, err := template.New("rathole-client").Parse(ratholeClientTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, clientData{VPSHost: vpsHost, Tunnels: tunnels}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// GenerateMachineSSHClientConfig generates a rathole client config for a single machine's SSH tunnel.
// If the machine has agent fields set, an additional service entry is appended
// so the VPS can reach the gopher-agent through the same rathole connection.
func GenerateMachineSSHClientConfig(vpsHost string, machine *db.Machine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[client]\nremote_addr = \"%s:2333\"\n\n", vpsHost)
	fmt.Fprintf(&b, "# gopher-machine-start: %s\n", machine.ID)
	fmt.Fprintf(&b, "[client.services.machine-%s-ssh]\n", machine.ID)
	fmt.Fprintf(&b, "type = \"tcp\"\n")
	fmt.Fprintf(&b, "token = \"%s\"\n", machine.RatholeSSHToken)
	fmt.Fprintf(&b, "local_addr = \"0.0.0.0:22\"\n")
	fmt.Fprintf(&b, "# gopher-machine-end: %s\n", machine.ID)

	if machine.AgentLocalPort > 0 && machine.AgentRatholeToken != "" {
		fmt.Fprintf(&b, "\n# gopher-machine-agent-start: %s\n", machine.ID)
		fmt.Fprintf(&b, "[client.services.machine-%s-agent]\n", machine.ID)
		fmt.Fprintf(&b, "type = \"tcp\"\n")
		fmt.Fprintf(&b, "token = \"%s\"\n", machine.AgentRatholeToken)
		fmt.Fprintf(&b, "local_addr = \"127.0.0.1:%d\"\n", machine.AgentLocalPort)
		fmt.Fprintf(&b, "# gopher-machine-agent-end: %s\n", machine.ID)
	}
	return b.String()
}

// GenerateRatholeServerConfig generates a complete rathole server config from scratch
// using Gopher-managed entry markers. Database is the single source of truth.
// Never appends to existing config; always regenerates completely.
//
// An optional bindIP argument (e.g. "203.0.113.10") restricts all public listeners
// to a specific IP instead of 0.0.0.0. Private tunnels always use 127.0.0.1.
func GenerateRatholeServerConfig(machines []db.Machine, tunnels []db.Tunnel, bindIP ...string) string {
	publicHost := resolvePublicHost(bindIP...)

	var buf strings.Builder
	managedEntries := 0

	// Write server section
	buf.WriteString("[server]\n")
	buf.WriteString(fmt.Sprintf("bind_addr = \"%s:2333\"\n", publicHost))

	// Write machine SSH tunnels with markers
	for _, m := range machines {
		if m.RatholeSSHToken == "" || m.TunnelPort == 0 {
			continue
		}
		buf.WriteString(fmt.Sprintf("\n# gopher-machine-start: %s\n", m.ID))
		buf.WriteString(fmt.Sprintf("[server.services.machine-%s-ssh]\n", m.ID))
		buf.WriteString(fmt.Sprintf("token = \"%s\"\n", m.RatholeSSHToken))
		sshBindHost := "127.0.0.1"
		if m.PublicSSH {
			sshBindHost = publicHost
		}
		buf.WriteString(fmt.Sprintf("bind_addr = \"%s:%d\"\n", sshBindHost, m.TunnelPort))
		buf.WriteString(fmt.Sprintf("# gopher-machine-end: %s\n", m.ID))
		managedEntries++

		// gopher-agent back-channel (only when the machine has been migrated).
		// Always bound to 127.0.0.1 — the agent is for the VPS to reach the
		// client, not for public consumption.
		if m.AgentRemotePort > 0 && m.AgentRatholeToken != "" {
			buf.WriteString(fmt.Sprintf("\n# gopher-machine-agent-start: %s\n", m.ID))
			buf.WriteString(fmt.Sprintf("[server.services.machine-%s-agent]\n", m.ID))
			buf.WriteString(fmt.Sprintf("token = \"%s\"\n", m.AgentRatholeToken))
			buf.WriteString(fmt.Sprintf("bind_addr = \"127.0.0.1:%d\"\n", m.AgentRemotePort))
			buf.WriteString(fmt.Sprintf("# gopher-machine-agent-end: %s\n", m.ID))
			managedEntries++
		}
	}

	// Write service tunnels with markers
	for _, t := range tunnels {
		if t.RatholePort == 0 {
			continue
		}
		token := t.RatholeToken
		if token == "" {
			token = t.ID // backward compat for old tunnels
		}
		buf.WriteString(fmt.Sprintf("\n# gopher-tunnel-start: %s\n", t.ID))
		buf.WriteString(fmt.Sprintf("[server.services.tunnel-%s]\n", t.ID))
		buf.WriteString(fmt.Sprintf("token = \"%s\"\n", token))
		// Private tunnels: 127.0.0.1 — only Caddy needs to reach them.
		// Public tunnels: bind_ip (or 0.0.0.0) — externally accessible.
		bindHost := publicHost
		if t.Private {
			bindHost = "127.0.0.1"
		}
		buf.WriteString(fmt.Sprintf("bind_addr = \"%s:%d\"\n", bindHost, t.RatholePort))
		if t.Transport == "udp" {
			buf.WriteString("type = \"udp\"\n")
		}
		buf.WriteString(fmt.Sprintf("# gopher-tunnel-end: %s\n", t.ID))
		managedEntries++
	}

	// Add placeholder if no entries to keep rathole happy (requires at least one service)
	if managedEntries == 0 {
		buf.WriteString("\n[server.services.placeholder]\n")
		buf.WriteString("token = \"placeholder\"\n")
		buf.WriteString(fmt.Sprintf("bind_addr = \"%s:52000\"\n", publicHost))
	}

	return buf.String()
}

// resolvePublicHost returns bindIP[0] if non-empty, otherwise "0.0.0.0".
func resolvePublicHost(bindIP ...string) string {
	if len(bindIP) > 0 && bindIP[0] != "" {
		return bindIP[0]
	}
	return "0.0.0.0"
}

// extractPortFromBindAddr extracts the port number from a "0.0.0.0:PORT" bind_addr string
func extractPortFromBindAddr(bindAddr string) int {
	if bindAddr == "" {
		return 0
	}
	parts := strings.Split(bindAddr, ":")
	if len(parts) != 2 {
		return 0
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0
	}
	return port
}
