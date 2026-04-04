CREATE TABLE IF NOT EXISTS firewall_rules (
    id          TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    protocol    TEXT NOT NULL DEFAULT 'tcp',
    port_range  TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '0.0.0.0/0',
    action      TEXT NOT NULL DEFAULT 'ACCEPT',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
