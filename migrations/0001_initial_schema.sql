-- Initial schema for Timox Server

CREATE TABLE IF NOT EXISTS users (
    uuid TEXT PRIMARY KEY,
    name TEXT,
    email TEXT,
    role TEXT NOT NULL DEFAULT 'child'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email IS NOT NULL;

CREATE TABLE IF NOT EXISTS app_limits (
    user_uuid TEXT NOT NULL,
    package_name TEXT NOT NULL,
    daily_limit_minutes INTEGER NOT NULL,
    blocked BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (user_uuid, package_name),
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS auth_tokens (
    token TEXT PRIMARY KEY,
    user_uuid TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS parent_children (
    parent_uuid TEXT NOT NULL,
    child_uuid TEXT NOT NULL,
    PRIMARY KEY (parent_uuid, child_uuid),
    FOREIGN KEY (parent_uuid) REFERENCES users(uuid) ON DELETE CASCADE,
    FOREIGN KEY (child_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS app_usage (
    user_uuid TEXT NOT NULL,
    package_name TEXT NOT NULL,
    usage_date TEXT NOT NULL,
    total_used_minutes INTEGER NOT NULL,
    app_name TEXT,
    PRIMARY KEY (user_uuid, package_name, usage_date),
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);
