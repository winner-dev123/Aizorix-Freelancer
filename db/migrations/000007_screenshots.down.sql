BEGIN;
DROP TABLE IF EXISTS screenshot_metadata;
DROP TABLE IF EXISTS screenshots;  -- drops all partitions
COMMIT;
