ALTER TABLE users RENAME COLUMN schedule_start TO blocking_start_time;
ALTER TABLE users RENAME COLUMN schedule_end TO blocking_end_time;
ALTER TABLE app_schedules RENAME COLUMN start_time TO blocking_start_time;
ALTER TABLE app_schedules RENAME COLUMN end_time TO blocking_end_time;
