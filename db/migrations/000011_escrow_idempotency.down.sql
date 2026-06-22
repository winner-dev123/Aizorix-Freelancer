-- 000011 (down) — drop the ledger zero-sum trigger and the escrow_allocations idempotency
-- indexes added in the up migration.
BEGIN;

DROP TRIGGER IF EXISTS trg_txn_group_balanced ON transactions;
DROP FUNCTION IF EXISTS assert_txn_group_balanced();

DROP INDEX IF EXISTS uq_alloc_billing_week_active;
DROP INDEX IF EXISTS uq_alloc_milestone_active;

COMMIT;
