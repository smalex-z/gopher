package config

import (
"bytes"
_ "embed"
"text/template"

"github.com/smalex-z/gopher/internal/db"
)

//go:embed templates/Caddyfile.tmpl
var caddyTemplate string

type caddyData struct {
Domain  string
Tunnels []db.Tunnel
}

func GenerateCaddyfile(vps db.VPSConfig, tunnels []db.Tunnel) (string, error) {
tmpl, err := template.New("caddyfile").Parse(caddyTemplate)
if err != nil {
return "", err
}

data := caddyData{
Domain:  vps.Domain,
Tunnels: tunnels,
}

var buf bytes.Buffer
if err := tmpl.Execute(&buf, data); err != nil {
return "", err
}
return buf.String(), nil
}
