-- 000013 (down) — drop the escrow funding idempotency table.
BEGIN;
DROP TABLE IF EXISTS escrow_fund_idempotency;
COMMIT;
