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
	FirewallMode   string    `json:"firewall_mode"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
