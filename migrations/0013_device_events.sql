CREATE TABLE IF NOT EXISTS device_events (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_uuid        TEXT NOT NULL,
    event_type       TEXT NOT NULL,
    event_timestamp  TEXT NOT NULL,
    data             TEXT NOT NULL DEFAULT '{}',
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_device_events_user ON device_events(user_uuid, created_at DESC);
