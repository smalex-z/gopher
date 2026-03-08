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
	Status          string     `json:"status"`
	LastSeen        *time.Time `json:"last_seen"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Tunnels         []Tunnel   `json:"tunnels,omitempty" gorm:"foreignKey:MachineID"`
}

type Tunnel struct {
	ID          string    `json:"id" gorm:"primaryKey"`
	MachineID   string    `json:"machine_id"`
	Name        string    `json:"name"`
	Subdomain   string    `json:"subdomain"`
	LocalPort   int       `json:"local_port"`
	RatholePort int       `json:"rathole_port"`
	Protocol    string    `json:"protocol"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BootstrapToken struct {
	ID        string     `json:"id" gorm:"primaryKey"`
	Token     string     `json:"token" gorm:"uniqueIndex"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at"`
	MachineID *string    `json:"machine_id"`
	CreatedAt time.Time  `json:"created_at"`
}

type AppSettings struct {
	ID             string    `json:"id" gorm:"primaryKey"`
	PasswordHash   string    `json:"-"`
	IsSetup        bool      `json:"is_setup"`
	Domain         string    `json:"domain"`
	LocalSetupDone bool      `json:"local_setup_done"`
	SSHPublicKey   string    `json:"ssh_public_key"`
	SSHPrivateKey  string    `json:"-" gorm:"column:ssh_private_key"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
