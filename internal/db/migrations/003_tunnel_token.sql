-- Add per-tunnel rathole auth token (shorter than the UUID id).
-- Backfill existing rows with their id so existing configs keep working.
ALTER TABLE tunnels ADD COLUMN rathole_token TEXT NOT NULL DEFAULT '';
UPDATE tunnels SET rathole_token = id WHERE rathole_token = '';
