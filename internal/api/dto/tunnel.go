package dto

type CreateTunnelRequest struct {
	MachineID            string `json:"machine_id"`
	Name                 string `json:"name"`
	Subdomain            string `json:"subdomain"`
	LocalPort            int    `json:"local_port"`
	RatholePort          int    `json:"rathole_port"` // 0 = auto-assign
	Transport            string `json:"transport"`    // "tcp" (default) or "udp"
	NoTLS                bool   `json:"no_tls"`       // skip Caddy TLS; plain http://
	Private              bool   `json:"private"`      // bind 127.0.0.1 instead of 0.0.0.0
	BotProtectionEnabled bool   `json:"bot_protection_enabled"`
	BotProtectionTTL     int    `json:"bot_protection_ttl"`      // seconds; 0 = default (86400)
	BotProtectionAllowIP string `json:"bot_protection_allow_ip"` // JSON array of CIDR/IP strings
	TLSSkipVerify        bool   `json:"tls_skip_verify"`
}

type UpdateTunnelRequest struct {
	Name                 string `json:"name"`
	Subdomain            string `json:"subdomain"`
	LocalPort            int    `json:"local_port"`
	Private              bool   `json:"private"`
	BotProtectionEnabled bool   `json:"bot_protection_enabled"`
	BotProtectionTTL     int    `json:"bot_protection_ttl"`
	BotProtectionAllowIP string `json:"bot_protection_allow_ip"`
	TLSSkipVerify        bool   `json:"tls_skip_verify"`
}
