-- Backfill rathole_token for tunnels created before the short-token change.
-- The column itself is added by AutoMigrate; this just fills empty values.
--
-- Renamed from 003_tunnel_token.sql so the numeric prefix is unique. The
-- UPDATE is idempotent — installs that already ran the 003-prefixed version
-- will re-run this once and find every relevant row already populated.
UPDATE tunnels SET rathole_token = id WHERE rathole_token = '';
