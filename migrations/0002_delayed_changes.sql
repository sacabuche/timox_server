ALTER TABLE users ADD COLUMN delayed_changes BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS pending_app_limits (
    user_uuid TEXT NOT NULL,
    package_name TEXT NOT NULL,
    daily_limit_minutes INTEGER NOT NULL,
    blocked BOOLEAN NOT NULL DEFAULT false,
    applies_at DATETIME NOT NULL,
    PRIMARY KEY (user_uuid, package_name),
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);
