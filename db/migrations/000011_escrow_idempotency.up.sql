-- 000011 — Escrow idempotency + ledger zero-sum assertion.
--
-- Two defense-in-depth changes for the money services:
--
--  (1) Natural-key idempotency for escrow_allocations: a non-refunded allocation is unique
--      per (escrow_id, milestone_id) and per (escrow_id, billing_week). This makes a repeated
--      milestone/week allocation collide at the database level so the escrow service can
--      return the prior result instead of reserving (and later releasing) the same funds
--      twice. Refunded allocations are excluded so a milestone can be re-allocated after a
--      refund.
--
--  (2) A DEFERRED CONSTRAINT TRIGGER asserting the double-entry invariant: every txn_group in
--      `transactions` must have SUM(amount_cents) = 0 at COMMIT. The application's WriteLegs
--      already validates this in Go; the trigger guarantees it at the database boundary even
--      if a future code path posts legs directly.
BEGIN;

-- ── (1) escrow_allocations natural-key idempotency ──────────────────────────
-- Partial unique indexes: at most one NON-refunded allocation per milestone / per week.
CREATE UNIQUE INDEX IF NOT EXISTS uq_alloc_milestone_active
    ON escrow_allocations (escrow_id, milestone_id)
    WHERE status <> 'refunded' AND milestone_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_alloc_billing_week_active
    ON escrow_allocations (escrow_id, billing_week)
    WHERE status <> 'refunded' AND billing_week IS NOT NULL;

-- ── (2) per-txn_group zero-sum ledger assertion ─────────────────────────────
-- The trigger function re-checks the sum for the txn_group of the affected row. Because the
-- constraint trigger is DEFERRABLE INITIALLY DEFERRED, it runs at COMMIT, after all legs of
-- the group have been inserted, so a transiently-unbalanced group mid-transaction is fine.
CREATE OR REPLACE FUNCTION assert_txn_group_balanced() RETURNS trigger AS $$
DECLARE
    grp   UUID;
    total BIGINT;
BEGIN
    grp := COALESCE(NEW.txn_group, OLD.txn_group);
    SELECT COALESCE(SUM(amount_cents), 0) INTO total
        FROM transactions
        WHERE txn_group = grp;
    IF total <> 0 THEN
        RAISE EXCEPTION 'unbalanced ledger txn_group=% sum=%', grp, total
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_txn_group_balanced
    AFTER INSERT OR UPDATE OR DELETE ON transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_txn_group_balanced();

COMMIT;
