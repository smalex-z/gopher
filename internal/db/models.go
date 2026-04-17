package db

import "time"

// BotSession records a browser that has passed the PoW challenge for a tunnel.
type BotSession struct {
	ID        string    `json:"id" gorm:"primaryKey"`
	TunnelID  string    `json:"tunnel_id" gorm:"index"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type VPSConfig struct {
	ID            string    `json:"id" gorm:"primaryKey"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	PrivateKey    string    `json:"private_key"`
	Domain        string    `json:"domain"`
	SSHPublicKey  string    `json:"ssh_public_key"`
	SSHPrivateKey string    `json:"ssh_private_key,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Machine struct {
	ID              string     `json:"id" gorm:"primaryKey"`
	Name            string     `json:"name"`
	Host            string     `json:"host"`
	Port            int        `json:"port"`
	Username        string     `json:"username"`
	PrivateKey      string     `json:"private_key,omitempty"`
	TunnelPort      int        `json:"tunnel_port"`
	RatholeSSHToken string     `json:"rathole_ssh_token,omitempty"`
	SSHKeyID        string     `json:"ssh_key_id" gorm:"index"`
	PublicSSH       bool       `json:"public_ssh"`
	Status          string     `json:"status"`
	PublicIP        string     `json:"public_ip"`
	LastSeen        *time.Time `json:"last_seen"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Tunnels         []Tunnel   `json:"tunnels,omitempty" gorm:"foreignKey:MachineID"`
}

type Tunnel struct {
	ID           string    `json:"id" gorm:"primaryKey"`
	MachineID    string    `json:"machine_id"`
	Name         string    `json:"name"`
	Subdomain    string    `json:"subdomain"`
	LocalPort    int       `json:"local_port"`
	RatholePort  int       `json:"rathole_port"`
	RatholeToken string    `json:"rathole_token"`
	Protocol     string    `json:"protocol"`
	Transport    string    `json:"transport"`  // "tcp" (default) or "udp"
	NoTLS        bool      `json:"no_tls"`     // skip Caddy HTTPS; use plain http://
	Private      bool      `json:"private"`    // bind 127.0.0.1 (VPS-local only) instead of 0.0.0.0
	// Bot protection — opt-in per tunnel, HTTP subdomain tunnels only.
	BotProtectionEnabled bool   `json:"bot_protection_enabled"`
	BotProtectionTTL     int    `json:"bot_protection_ttl"`      // session TTL in seconds; 0 = default (86400)
	BotProtectionAllowIP string `json:"bot_protection_allow_ip"` // JSON array of CIDR/IP strings
	Status       string    `json:"status"`
	Managed      bool      `json:"managed,omitempty" gorm:"-"`
	Kind         string    `json:"kind,omitempty" gorm:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type BootstrapToken struct {
	ID         string     `json:"id" gorm:"primaryKey"`
	Token      string     `json:"token" gorm:"uniqueIndex"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at"`
	MachineID  *string    `json:"machine_id"`
	TunnelPort int        `json:"tunnel_port"`
	SSHKeyID   string     `json:"ssh_key_id"`
	PublicSSH  bool       `json:"public_ssh"`
	CreatedAt  time.Time  `json:"created_at"`
}

type AppSettings struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	PasswordHash   string    `json:"-"`
	IsSetup        bool      `json:"is_setup"`
	Domain         string    `json:"domain"`
	// ServerHost is the hostname or IP used as the rathole remote_addr in client
	// configs. When Caddy is enabled this equals Domain. When Caddy is skipped it
	// holds the manually-provided VPS hostname/IP so client configs can still be
	// generated even though Domain is empty.
	ServerHost     string    `json:"server_host"`
	LocalSetupDone bool      `json:"local_setup_done"`
	// FirewallMode is one of "gopher" (Gopher manages iptables), "manual" (user manages),
	// or "none" (no firewall). Empty string means the wizard step has not run yet.
	FirewallMode      string    `json:"firewall_mode"`
	// DashboardPrivate restricts the dashboard port to localhost (VPS-only) when true.
	// Zero value (false) keeps it publicly reachable — safe migration default.
	DashboardPrivate  bool      `json:"dashboard_private"`
	// CustomIPTables holds raw iptables rule specs (one per line, everything after
	// "iptables ") that are applied to the GOPHER_CUSTOM chain. Flushed and
	// re-applied whenever this field changes.
	CustomIPTables  string    `json:"custom_iptables"`
	// TOTP 2FA fields
	TOTPSecret      string    `json:"-"`            // base32-encoded TOTP secret; empty means not enrolled
	TOTPEnabled     bool      `json:"totp_enabled"` // true once confirmed via first successful code
	TOTPBackupCodes string    `json:"-"`            // JSON array of bcrypt-hashed one-time codes
	// Fail2ban configuration (written to /etc/fail2ban/jail.d/gopher.conf on save)
	Fail2banSetupDone bool   `json:"fail2ban_setup_done"` // true once fail2ban has been installed and configured
	Fail2banMaxRetry  int    `json:"fail2ban_max_retry"`  // default 5
	Fail2banFindTime  int    `json:"fail2ban_find_time"`  // seconds, default 300
	Fail2banBanTime   int    `json:"fail2ban_ban_time"`   // seconds, default 3600
	Fail2banIgnoreIPs string `json:"fail2ban_ignore_ips"` // JSON array of whitelisted CIDRs/IPs
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// FirewallRule is a user-defined rule applied to GOPHER_CUSTOM.
// Either RawSpec is set (raw mode) or the structured fields are used.
type FirewallRule struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	Description string    `json:"description"`
	Raw         bool      `json:"raw"`         // if true, RawSpec is used as-is
	RawSpec     string    `json:"raw_spec"`    // e.g. "-s 1.2.3.4 -p tcp --dport 80 -j ACCEPT"
	Protocol    string    `json:"protocol"`    // "tcp", "udp", "all", "icmp"
	PortRange   string    `json:"port_range"`  // "80", "8000:9000", "" = any
	Source      string    `json:"source"`      // CIDR, e.g. "0.0.0.0/0"
	Action      string    `json:"action"`      // "ACCEPT", "DROP", "REJECT"
	CreatedAt   time.Time `json:"created_at"`
}

type SSHKey struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	Name       string    `json:"name"`
	PublicKey  string    `json:"public_key"`
	PrivateKey string    `json:"-" gorm:"column:private_key"`
	IsDefault  bool      `json:"is_default"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
