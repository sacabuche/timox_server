CREATE TABLE IF NOT EXISTS app_schedules (
    user_uuid    TEXT NOT NULL,
    package_name TEXT NOT NULL,
    blocking_start_time TEXT NOT NULL, -- HH:MM, e.g. "09:00"
    blocking_end_time   TEXT NOT NULL, -- HH:MM, e.g. "21:00"
    PRIMARY KEY (user_uuid, package_name),
    FOREIGN KEY (user_uuid) REFERENCES users(uuid) ON DELETE CASCADE
);

ALTER TABLE users ADD COLUMN blocking_start_time TEXT; -- HH:MM, e.g. "08:00"
ALTER TABLE users ADD COLUMN blocking_end_time   TEXT; -- HH:MM, e.g. "20:00"
