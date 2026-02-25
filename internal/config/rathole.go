package config

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/smalex-z/gopher/internal/db"
)

const ratholeServerTemplate = `[server]
bind_addr = "0.0.0.0:2333"

{{range .Tunnels}}
[server.services.{{.Name}}]
token = "{{.Token}}"
bind_addr = "0.0.0.0:{{.RemotePort}}"
{{end}}
`

const ratholeClientTemplate = `[client]
remote_addr = "{{.VPSHost}}:2333"

{{range .Tunnels}}
[client.services.{{.Name}}]
token = "{{.Token}}"
local_addr = "{{.LocalHost}}:{{.LocalPort}}"
{{end}}
`

type ratholeServerData struct {
	Tunnels []db.Tunnel
}

type ratholeClientData struct {
	VPSHost string
	Tunnels []db.Tunnel
}

func GenerateRatholeServerConfig(tunnels []db.Tunnel) (string, error) {
	tmpl, err := template.New("rathole-server").Parse(ratholeServerTemplate)
	if err != nil {
		return "", fmt.Errorf("parse rathole server template: %w", err)
	}
	data := ratholeServerData{Tunnels: tunnels}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute rathole server template: %w", err)
	}
	return buf.String(), nil
}

func GenerateRatholeClientConfig(vpsHost string, tunnels []db.Tunnel) (string, error) {
	tmpl, err := template.New("rathole-client").Parse(ratholeClientTemplate)
	if err != nil {
		return "", fmt.Errorf("parse rathole client template: %w", err)
	}
	data := ratholeClientData{VPSHost: vpsHost, Tunnels: tunnels}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute rathole client template: %w", err)
	}
	return buf.String(), nil
}
