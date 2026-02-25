package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"

	"github.com/smalex-z/gopher/internal/db"
)

//go:embed templates/rathole-server.toml.tmpl
var ratholeServerTemplate string

//go:embed templates/rathole-client.toml.tmpl
var ratholeClientTemplate string

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
