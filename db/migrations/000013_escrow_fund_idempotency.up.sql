-- 000013 — Escrow funding idempotency.
--
-- FundEscrow adjusts an escrow account's held_cents for a real client deposit. Without an
-- idempotency key a replayed fund request inflates held_cents again with no corresponding
-- deposit. This records each accepted (contract_id, idempotency_key) pair so a duplicate key
-- collides at the database level and the escrow service returns the existing escrow as a
-- no-op instead of crediting held_cents twice.
BEGIN;

CREATE TABLE IF NOT EXISTS escrow_fund_idempotency (
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    escrow_id       UUID NOT NULL REFERENCES escrow_accounts(id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (contract_id, idempotency_key)
);

COMMIT;
