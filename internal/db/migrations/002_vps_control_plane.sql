-- Add SSH keypair to VPS config (VPS generates an SSH keypair, installs public key on machines)
ALTER TABLE vps_configs ADD COLUMN ssh_public_key TEXT NOT NULL DEFAULT '';
ALTER TABLE vps_configs ADD COLUMN ssh_private_key TEXT NOT NULL DEFAULT '';

-- Add reverse-tunnel fields to machines
-- tunnel_port: the port on VPS that rathole maps to machine's SSH port 22
-- rathole_ssh_token: the rathole auth token for this machine's SSH tunnel
ALTER TABLE machines ADD COLUMN tunnel_port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machines ADD COLUMN rathole_ssh_token TEXT NOT NULL DEFAULT '';

-- Bootstrap tokens for self-registration
CREATE TABLE IF NOT EXISTS bootstrap_tokens (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    expires_at DATETIME NOT NULL,
    used_at DATETIME,
    machine_id TEXT REFERENCES machines(id),
    created_at DATETIME NOT NULL
);
