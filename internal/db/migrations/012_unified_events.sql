-- Unify activity_events into the new events table.
--
-- AutoMigrate runs before this and creates the events table from the Event
-- struct (with severity, source, kind, actor, resource_*, ip, message,
-- metadata columns). This migration copies any existing activity_events rows
-- into events with sensible defaults derived from the kind, then drops the
-- old table.
--
-- Safe on fresh installs (activity_events doesn't exist) and on installs
-- predating the activity feature: the CREATE TABLE IF NOT EXISTS gives the
-- INSERT a valid source even when there's nothing to migrate, and the final
-- DROP cleans up regardless.

CREATE TABLE IF NOT EXISTS activity_events (
    id TEXT PRIMARY KEY,
    kind TEXT,
    resource_id TEXT,
    name TEXT,
    created_at DATETIME
);

INSERT OR IGNORE INTO events (
    id, created_at, severity, source, kind,
    resource_type, resource_id, resource_name,
    actor, ip, message, metadata
)
SELECT
    id,
    created_at,
    'info' AS severity,
    CASE
        WHEN kind LIKE 'machine%' THEN 'machine'
        WHEN kind LIKE 'tunnel%'  THEN 'tunnel'
        ELSE 'system'
    END AS source,
    kind,
    CASE
        WHEN kind LIKE 'machine%' THEN 'machine'
        WHEN kind LIKE 'tunnel%'  THEN 'tunnel'
        ELSE ''
    END AS resource_type,
    resource_id,
    name AS resource_name,
    'system' AS actor,
    '' AS ip,
    CASE
        WHEN kind = 'machine_registered' THEN 'Machine ' || name || ' registered'
        WHEN kind = 'machine_deleted'    THEN 'Machine ' || name || ' deleted'
        WHEN kind = 'tunnel_created'     THEN 'Tunnel '  || name || ' created'
        WHEN kind = 'tunnel_deleted'     THEN 'Tunnel '  || name || ' deleted'
        ELSE kind
    END AS message,
    '' AS metadata
FROM activity_events;

DROP TABLE IF EXISTS activity_events;
