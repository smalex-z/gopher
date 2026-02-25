package db

import (
	"database/sql"
	"time"
)

type Machine struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	User      string    `json:"user"`
	SSHKey    string    `json:"ssh_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func ListMachines(db *sql.DB) ([]Machine, error) {
	rows, err := db.Query(`SELECT id, name, host, port, user, ssh_key, created_at, updated_at FROM machines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var machines []Machine
	for rows.Next() {
		var m Machine
		if err := rows.Scan(&m.ID, &m.Name, &m.Host, &m.Port, &m.User, &m.SSHKey, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		machines = append(machines, m)
	}
	return machines, rows.Err()
}

func GetMachine(db *sql.DB, id int) (*Machine, error) {
	row := db.QueryRow(`SELECT id, name, host, port, user, ssh_key, created_at, updated_at FROM machines WHERE id = ?`, id)
	m := &Machine{}
	err := row.Scan(&m.ID, &m.Name, &m.Host, &m.Port, &m.User, &m.SSHKey, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

func CreateMachine(db *sql.DB, m *Machine) (int64, error) {
	res, err := db.Exec(`INSERT INTO machines (name, host, port, user, ssh_key) VALUES (?, ?, ?, ?, ?)`,
		m.Name, m.Host, m.Port, m.User, m.SSHKey)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateMachine(db *sql.DB, m *Machine) error {
	_, err := db.Exec(`UPDATE machines SET name=?, host=?, port=?, user=?, ssh_key=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		m.Name, m.Host, m.Port, m.User, m.SSHKey, m.ID)
	return err
}

func DeleteMachine(db *sql.DB, id int) error {
	_, err := db.Exec(`DELETE FROM machines WHERE id=?`, id)
	return err
}
