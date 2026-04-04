package dto

type CreateTunnelRequest struct {
	MachineID   string `json:"machine_id"`
	Name        string `json:"name"`
	Subdomain   string `json:"subdomain"`
	LocalPort   int    `json:"local_port"`
	RatholePort int    `json:"rathole_port"` // 0 = auto-assign
	Transport   string `json:"transport"`    // "tcp" (default) or "udp"
	NoTLS       bool   `json:"no_tls"`       // skip Caddy TLS; plain http://
	Private     bool   `json:"private"`      // bind 127.0.0.1 instead of 0.0.0.0
}

type UpdateTunnelRequest struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
	LocalPort int    `json:"local_port"`
	Private   bool   `json:"private"`
}
