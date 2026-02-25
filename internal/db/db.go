package db

import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func Migrate(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS vps_config (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    host        TEXT NOT NULL,
    port        INTEGER NOT NULL DEFAULT 22,
    user        TEXT NOT NULL,
    ssh_key     TEXT NOT NULL,
    domain      TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS machines (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    host        TEXT NOT NULL,
    port        INTEGER NOT NULL DEFAULT 22,
    user        TEXT NOT NULL,
    ssh_key     TEXT NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS tunnels (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    subdomain       TEXT NOT NULL UNIQUE,
    machine_id      INTEGER NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    local_host      TEXT NOT NULL DEFAULT "127.0.0.1",
    local_port      INTEGER NOT NULL,
    remote_port     INTEGER NOT NULL UNIQUE,
    token           TEXT NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
	_, err := db.Exec(schema)
	return err
}
