package db

import "time"

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
	LocalSetupDone bool      `json:"local_setup_done"`
	// FirewallMode is one of "gopher" (Gopher manages iptables), "manual" (user manages),
	// or "none" (no firewall). Empty string means the wizard step has not run yet.
	FirewallMode      string    `json:"firewall_mode"`
	// DashboardPrivate restricts port 8080 to localhost (VPS-only) when true.
	// Zero value (false) keeps port 8080 publicly reachable — safe migration default.
	DashboardPrivate  bool      `json:"dashboard_private"`
	// CustomIPTables holds raw iptables rule specs (one per line, everything after
	// "iptables ") that are applied to the GOPHER_CUSTOM chain. Flushed and
	// re-applied whenever this field changes.
	CustomIPTables  string    `json:"custom_iptables"`
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
