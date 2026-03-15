-- Replace blocked boolean with block_type int on app_limits and pending_app_limits.
-- Values: 0=normal, 1=blocked, 2=global_exempt, 3=unrestricted

ALTER TABLE app_limits ADD COLUMN block_type INTEGER NOT NULL DEFAULT 0;
UPDATE app_limits SET block_type = 1 WHERE blocked = 1;
ALTER TABLE app_limits DROP COLUMN blocked;

ALTER TABLE pending_app_limits ADD COLUMN block_type INTEGER NOT NULL DEFAULT 0;
UPDATE pending_app_limits SET block_type = 1 WHERE blocked = 1;
ALTER TABLE pending_app_limits DROP COLUMN blocked;
