BEGIN;

DROP INDEX IF EXISTS uq_screenshots_slice;

DROP TRIGGER IF EXISTS trg_audit_logs_append_only ON audit_logs;
DROP TRIGGER IF EXISTS trg_transactions_append_only ON transactions;
DROP FUNCTION IF EXISTS forbid_row_mutation();

DROP TABLE IF EXISTS audit_logs_default;
DROP TABLE IF EXISTS messages_default;
DROP TABLE IF EXISTS screenshots_default;
DROP TABLE IF EXISTS activity_logs_default;

COMMIT;
