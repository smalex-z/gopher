CREATE TABLE IF NOT EXISTS vps_configs (
    id TEXT PRIMARY KEY,
    host TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 22,
    username TEXT NOT NULL,
    private_key TEXT NOT NULL,
    domain TEXT NOT NULL,
    created_at DATETIME,
    updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS machines (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 22,
    username TEXT NOT NULL,
    private_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    last_seen DATETIME,
    created_at DATETIME,
    updated_at DATETIME
);

CREATE TABLE IF NOT EXISTS tunnels (
    id TEXT PRIMARY KEY,
    machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    subdomain TEXT NOT NULL UNIQUE,
    local_port INTEGER NOT NULL,
    rathole_port INTEGER NOT NULL UNIQUE,
    protocol TEXT NOT NULL DEFAULT 'http',
    status TEXT NOT NULL DEFAULT 'inactive',
    created_at DATETIME,
    updated_at DATETIME
);
