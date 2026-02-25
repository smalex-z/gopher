package config

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/smalex-z/gopher/internal/db"
)

const caddyTemplate = `{
    email admin@{{.Domain}}
}

{{range .Tunnels}}
{{.Subdomain}}.{{$.Domain}} {
    reverse_proxy localhost:{{.RemotePort}}
}
{{end}}
`

type caddyData struct {
	Domain  string
	Tunnels []db.Tunnel
}

func GenerateCaddyfile(domain string, tunnels []db.Tunnel) (string, error) {
	tmpl, err := template.New("caddy").Parse(caddyTemplate)
	if err != nil {
		return "", fmt.Errorf("parse caddy template: %w", err)
	}
	data := caddyData{Domain: domain, Tunnels: tunnels}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute caddy template: %w", err)
	}
	return buf.String(), nil
}
