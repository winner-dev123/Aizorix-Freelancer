-- 000008 — Escrow + payments + double-entry ledger (owned by `escrow` and `payment`).
-- Money is BIGINT minor units. The ledger is append-only; balances are derived/checked.
BEGIN;

-- ── Stripe payout accounts (Connect) ────────────────────────────────────────
CREATE TABLE payout_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL DEFAULT 'stripe',
    stripe_account_id TEXT,                   -- acct_...
    status          TEXT NOT NULL DEFAULT 'pending', -- pending|verified|restricted
    payouts_enabled BOOLEAN NOT NULL DEFAULT false,
    default_currency CHAR(3) NOT NULL DEFAULT 'USD',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, provider)
);
CREATE TRIGGER trg_payout_updated BEFORE UPDATE ON payout_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── Escrow accounts: one per contract ───────────────────────────────────────
CREATE TABLE escrow_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL UNIQUE REFERENCES contracts(id) ON DELETE CASCADE,
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    -- Derived balances (kept in sync with escrow_ledger; reconciled nightly).
    held_cents      BIGINT NOT NULL DEFAULT 0 CHECK (held_cents >= 0),
    released_cents  BIGINT NOT NULL DEFAULT 0 CHECK (released_cents >= 0),
    refunded_cents  BIGINT NOT NULL DEFAULT 0 CHECK (refunded_cents >= 0),
    status          escrow_status NOT NULL DEFAULT 'held',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_escrow_updated BEFORE UPDATE ON escrow_accounts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Allocation of escrow funds to a milestone (fixed) or billing week (hourly).
CREATE TABLE escrow_allocations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    escrow_id       UUID NOT NULL REFERENCES escrow_accounts(id) ON DELETE CASCADE,
    milestone_id    UUID REFERENCES milestones(id),
    billing_week    TEXT,
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    status          TEXT NOT NULL DEFAULT 'held', -- held|released|refunded
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at     TIMESTAMPTZ,
    CONSTRAINT alloc_target CHECK (milestone_id IS NOT NULL OR billing_week IS NOT NULL)
);
CREATE INDEX idx_alloc_escrow ON escrow_allocations (escrow_id, status);

-- ── Payments (client charges via Stripe PaymentIntents) ─────────────────────
CREATE TABLE payments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID REFERENCES contracts(id),
    payer_id        UUID NOT NULL REFERENCES users(id),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    status          payment_status NOT NULL DEFAULT 'processing',
    stripe_payment_intent_id TEXT,
    stripe_charge_id TEXT,
    -- Idempotency key supplied by caller to dedupe retries.
    idempotency_key TEXT,
    failure_reason  TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (idempotency_key)
);
CREATE INDEX idx_payments_contract ON payments (contract_id);
CREATE UNIQUE INDEX uq_payments_pi ON payments (stripe_payment_intent_id)
    WHERE stripe_payment_intent_id IS NOT NULL;
CREATE TRIGGER trg_payments_updated BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── Withdrawals (freelancer payouts) ────────────────────────────────────────
CREATE TABLE withdrawals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    freelancer_id   UUID NOT NULL REFERENCES users(id),
    payout_account_id UUID NOT NULL REFERENCES payout_accounts(id),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    status          withdrawal_status NOT NULL DEFAULT 'requested',
    stripe_transfer_id TEXT,
    stripe_payout_id   TEXT,
    failure_reason  TEXT,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at         TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_withdrawals_freelancer ON withdrawals (freelancer_id, status);
CREATE TRIGGER trg_withdrawals_updated BEFORE UPDATE ON withdrawals
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── Double-entry ledger: every cent movement is two balanced rows ───────────
-- account_kind names virtual accounts: 'client_funding','escrow','freelancer_balance',
-- 'platform_fee','stripe_clearing','refunds'. Sum of all postings per txn = 0.
CREATE TABLE transactions (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    txn_group       UUID NOT NULL,            -- groups the balanced legs of one operation
    type            txn_type NOT NULL,
    account_kind    TEXT NOT NULL,
    account_ref     UUID,                     -- user_id / escrow_id / contract_id depending on kind
    contract_id     UUID,
    -- Signed amount in minor units: debit negative, credit positive (per account).
    amount_cents    BIGINT NOT NULL,
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    payment_id      UUID REFERENCES payments(id),
    withdrawal_id   UUID REFERENCES withdrawals(id),
    memo            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_txn_group ON transactions (txn_group);
CREATE INDEX idx_txn_account ON transactions (account_kind, account_ref, created_at);
CREATE INDEX idx_txn_contract ON transactions (contract_id, created_at);

-- ── Stripe webhook idempotency / audit ──────────────────────────────────────
CREATE TABLE stripe_events (
    id              TEXT PRIMARY KEY,         -- Stripe event id (evt_...)
    type            TEXT NOT NULL,
    payload         JSONB NOT NULL,
    processed_at    TIMESTAMPTZ,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE reconciliation_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_date        DATE NOT NULL,
    ledger_balance_cents BIGINT NOT NULL,
    stripe_balance_cents BIGINT NOT NULL,
    discrepancy_cents BIGINT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'ok', -- ok|discrepancy|investigating
    details         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
