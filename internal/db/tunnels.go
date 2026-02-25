package db

import (
	"database/sql"
	"time"
)

type Tunnel struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	Subdomain  string    `json:"subdomain"`
	MachineID  int       `json:"machine_id"`
	LocalHost  string    `json:"local_host"`
	LocalPort  int       `json:"local_port"`
	RemotePort int       `json:"remote_port"`
	Token      string    `json:"token"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func ListTunnels(db *sql.DB) ([]Tunnel, error) {
	rows, err := db.Query(`SELECT id, name, subdomain, machine_id, local_host, local_port, remote_port, token, enabled, created_at, updated_at FROM tunnels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tunnels []Tunnel
	for rows.Next() {
		var t Tunnel
		var enabled int
		if err := rows.Scan(&t.ID, &t.Name, &t.Subdomain, &t.MachineID, &t.LocalHost, &t.LocalPort, &t.RemotePort, &t.Token, &enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		tunnels = append(tunnels, t)
	}
	return tunnels, rows.Err()
}

func GetTunnel(db *sql.DB, id int) (*Tunnel, error) {
	row := db.QueryRow(`SELECT id, name, subdomain, machine_id, local_host, local_port, remote_port, token, enabled, created_at, updated_at FROM tunnels WHERE id=?`, id)
	t := &Tunnel{}
	var enabled int
	err := row.Scan(&t.ID, &t.Name, &t.Subdomain, &t.MachineID, &t.LocalHost, &t.LocalPort, &t.RemotePort, &t.Token, &enabled, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	t.Enabled = enabled == 1
	return t, err
}

func CreateTunnel(db *sql.DB, t *Tunnel) (int64, error) {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	res, err := db.Exec(`INSERT INTO tunnels (name, subdomain, machine_id, local_host, local_port, remote_port, token, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Subdomain, t.MachineID, t.LocalHost, t.LocalPort, t.RemotePort, t.Token, enabled)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateTunnel(db *sql.DB, t *Tunnel) error {
	enabled := 0
	if t.Enabled {
		enabled = 1
	}
	_, err := db.Exec(`UPDATE tunnels SET name=?, subdomain=?, machine_id=?, local_host=?, local_port=?, remote_port=?, token=?, enabled=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		t.Name, t.Subdomain, t.MachineID, t.LocalHost, t.LocalPort, t.RemotePort, t.Token, enabled, t.ID)
	return err
}

func DeleteTunnel(db *sql.DB, id int) error {
	_, err := db.Exec(`DELETE FROM tunnels WHERE id=?`, id)
	return err
}

func ListTunnelsByMachine(db *sql.DB, machineID int) ([]Tunnel, error) {
	rows, err := db.Query(`SELECT id, name, subdomain, machine_id, local_host, local_port, remote_port, token, enabled, created_at, updated_at FROM tunnels WHERE machine_id=? ORDER BY name`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tunnels []Tunnel
	for rows.Next() {
		var t Tunnel
		var enabled int
		if err := rows.Scan(&t.ID, &t.Name, &t.Subdomain, &t.MachineID, &t.LocalHost, &t.LocalPort, &t.RemotePort, &t.Token, &enabled, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled == 1
		tunnels = append(tunnels, t)
	}
	return tunnels, rows.Err()
}
