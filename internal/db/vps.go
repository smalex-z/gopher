package db

import (
	"database/sql"
	"time"
)

type VPSConfig struct {
	ID        int       `json:"id"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	User      string    `json:"user"`
	SSHKey    string    `json:"ssh_key"`
	Domain    string    `json:"domain"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func GetVPS(db *sql.DB) (*VPSConfig, error) {
	row := db.QueryRow(`SELECT id, host, port, user, ssh_key, domain, created_at, updated_at FROM vps_config WHERE id = 1`)
	v := &VPSConfig{}
	err := row.Scan(&v.ID, &v.Host, &v.Port, &v.User, &v.SSHKey, &v.Domain, &v.CreatedAt, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func UpsertVPS(db *sql.DB, v *VPSConfig) error {
	_, err := db.Exec(`
INSERT INTO vps_config (id, host, port, user, ssh_key, domain, updated_at)
VALUES (1, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET
    host=excluded.host, port=excluded.port, user=excluded.user,
    ssh_key=excluded.ssh_key, domain=excluded.domain, updated_at=excluded.updated_at`,
		v.Host, v.Port, v.User, v.SSHKey, v.Domain)
	return err
}
