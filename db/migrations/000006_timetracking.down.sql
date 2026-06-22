BEGIN;
DROP TABLE IF EXISTS manual_time_requests;
DROP TABLE IF EXISTS timesheets;
DROP TABLE IF EXISTS activity_logs;  -- drops all partitions
DROP TABLE IF EXISTS time_slices;
DROP TABLE IF EXISTS work_sessions;
COMMIT;
