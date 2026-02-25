package db

import "time"

type VPSConfig struct {
ID         string    `json:"id" gorm:"primaryKey"`
Host       string    `json:"host"`
Port       int       `json:"port"`
Username   string    `json:"username"`
PrivateKey string    `json:"private_key"`
Domain     string    `json:"domain"`
CreatedAt  time.Time `json:"created_at"`
UpdatedAt  time.Time `json:"updated_at"`
}

type Machine struct {
ID         string     `json:"id" gorm:"primaryKey"`
Name       string     `json:"name"`
Host       string     `json:"host"`
Port       int        `json:"port"`
Username   string     `json:"username"`
PrivateKey string     `json:"private_key"`
Status     string     `json:"status"`
LastSeen   *time.Time `json:"last_seen"`
CreatedAt  time.Time  `json:"created_at"`
UpdatedAt  time.Time  `json:"updated_at"`
Tunnels    []Tunnel   `json:"tunnels,omitempty" gorm:"foreignKey:MachineID"`
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
