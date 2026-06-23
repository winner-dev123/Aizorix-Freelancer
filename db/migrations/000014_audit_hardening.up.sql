-- 000014 — Harden the partitioned + append-only tables (audit wave 3).
--
--   #6  Range-partitioned tables only pre-create the current+next month and there is no
--       scheduled partition job, so inserts fail once those elapse. Add DEFAULT partitions so
--       out-of-range rows land there instead of erroring (critical for audit_logs, whose write
--       shares the transaction with the privileged operation).
--   #13 Enforce append-only on the ledger (transactions) and audit_logs: block row UPDATE/DELETE
--       so a balanced ledger group can't be silently erased and audit rows can't be edited.
--       Retention is by partition DROP / compliance DDL, not row DELETE, so it is unaffected.
--   #2  Backstop screenshot upload-slot idempotency with a unique index on the slice.
BEGIN;

-- #6 — default partitions (no-op if a future-partition job is later added).
CREATE TABLE IF NOT EXISTS activity_logs_default PARTITION OF activity_logs DEFAULT;
CREATE TABLE IF NOT EXISTS screenshots_default   PARTITION OF screenshots   DEFAULT;
CREATE TABLE IF NOT EXISTS messages_default      PARTITION OF messages      DEFAULT;
CREATE TABLE IF NOT EXISTS audit_logs_default    PARTITION OF audit_logs    DEFAULT;

-- #13 — append-only guard.
CREATE OR REPLACE FUNCTION forbid_row_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'append-only table %: % is not permitted', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_transactions_append_only ON transactions;
CREATE TRIGGER trg_transactions_append_only
    BEFORE UPDATE OR DELETE ON transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_row_mutation();

DROP TRIGGER IF EXISTS trg_audit_logs_append_only ON audit_logs;
CREATE TRIGGER trg_audit_logs_append_only
    BEFORE UPDATE OR DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION forbid_row_mutation();

-- #2 — one screenshot row per slice (the index must include the partition key captured_at;
-- NULL slice_ids — online captures — remain distinct).
CREATE UNIQUE INDEX IF NOT EXISTS uq_screenshots_slice ON screenshots (slice_id, captured_at);

COMMIT;
