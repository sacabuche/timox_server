CREATE TABLE IF NOT EXISTS apps (
    package_name TEXT PRIMARY KEY,
    app_name     TEXT,
    icon_path    TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pending_icons (
    id           TEXT PRIMARY KEY,
    user_uuid    TEXT NOT NULL,
    package_name TEXT NOT NULL,
    submitted_at TEXT NOT NULL,
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pending_icons_user_pkg ON pending_icons(user_uuid, package_name);
