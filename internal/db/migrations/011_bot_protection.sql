ALTER TABLE tunnels ADD COLUMN bot_protection_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tunnels ADD COLUMN bot_protection_ttl INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tunnels ADD COLUMN bot_protection_allow_ip TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS bot_sessions (
    id TEXT PRIMARY KEY,
    tunnel_id TEXT NOT NULL,
    ip TEXT NOT NULL,
    user_agent TEXT NOT NULL,
    issued_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_tunnel_id ON bot_sessions(tunnel_id);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_expires_at ON bot_sessions(expires_at);
