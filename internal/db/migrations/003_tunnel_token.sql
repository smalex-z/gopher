-- Backfill rathole_token for tunnels created before the short-token change.
-- The column itself is added by AutoMigrate; this just fills empty values.
UPDATE tunnels SET rathole_token = id WHERE rathole_token = '';
