package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/template"

	"github.com/smalex-z/gopher/internal/db"
)

//go:embed templates/rathole-server.toml.tmpl
var ratholeServerTemplate string

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

type serverData struct {
	Tunnels  []db.Tunnel
	Machines []db.Machine
}

type clientData struct {
	VPSHost string
	Tunnels []db.Tunnel
}

func GenerateServerConfig(tunnels []db.Tunnel, machines []db.Machine) (string, error) {
	tmpl, err := template.New("rathole-server").Parse(ratholeServerTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, serverData{Tunnels: tunnels, Machines: machines}); err != nil {
		return "", err
	}
	return buf.String(), nil
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
func GenerateMachineSSHClientConfig(vpsHost string, machine *db.Machine) string {
	return fmt.Sprintf(`[client]
remote_addr = "%s:2333"

[client.services.machine-%s-ssh]
type = "tcp"
token = "%s"
local_addr = "0.0.0.0:22"
`, vpsHost, machine.ID, machine.RatholeSSHToken)
}

// GenerateRatholeServerConfig generates a complete rathole server config from scratch
// using Gopher-managed entry markers. Database is the single source of truth.
// Never appends to existing config; always regenerates completely.
func GenerateRatholeServerConfig(machines []db.Machine, tunnels []db.Tunnel) string {
	var buf strings.Builder

	// Write server section
	buf.WriteString("[server]\n")
	buf.WriteString("bind_addr = \"0.0.0.0:2333\"\n")

	// Write machine SSH tunnels with markers
	for _, m := range machines {
		if m.RatholeSSHToken == "" || m.TunnelPort == 0 {
			continue
		}
		buf.WriteString(fmt.Sprintf("\n# gopher-machine-start: %s\n", m.ID))
		buf.WriteString(fmt.Sprintf("[server.services.machine-%s-ssh]\n", m.ID))
		buf.WriteString(fmt.Sprintf("token = \"%s\"\n", m.RatholeSSHToken))
		buf.WriteString(fmt.Sprintf("bind_addr = \"0.0.0.0:%d\"\n", m.TunnelPort))
		buf.WriteString(fmt.Sprintf("# gopher-machine-end: %s\n", m.ID))
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
		buf.WriteString(fmt.Sprintf("bind_addr = \"0.0.0.0:%d\"\n", t.RatholePort))
		buf.WriteString(fmt.Sprintf("# gopher-tunnel-end: %s\n", t.ID))
	}

	// Add placeholder if no entries to keep rathole happy (requires at least one service)
	if len(machines) == 0 && len(tunnels) == 0 {
		buf.WriteString("\n[server.services.placeholder]\n")
		buf.WriteString("token = \"placeholder\"\n")
		buf.WriteString("bind_addr = \"0.0.0.0:52000\"\n")
	}

	return buf.String()
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

// extractPortFromAddr extracts the port number from "host:PORT" format
func extractPortFromAddr(addr string) int {
	if addr == "" {
		return 0
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}
