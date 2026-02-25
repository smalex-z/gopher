package config

import (
"bytes"
_ "embed"
"text/template"

"github.com/smalex-z/gopher/internal/db"
)

//go:embed templates/rathole-server.toml.tmpl
var ratholeServerTemplate string

//go:embed templates/rathole-client.toml.tmpl
var ratholeClientTemplate string

type serverData struct {
Tunnels []db.Tunnel
}

type clientData struct {
VPSHost string
Tunnels []db.Tunnel
}

func GenerateServerConfig(tunnels []db.Tunnel) (string, error) {
tmpl, err := template.New("rathole-server").Parse(ratholeServerTemplate)
if err != nil {
return "", err
}

var buf bytes.Buffer
if err := tmpl.Execute(&buf, serverData{Tunnels: tunnels}); err != nil {
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
