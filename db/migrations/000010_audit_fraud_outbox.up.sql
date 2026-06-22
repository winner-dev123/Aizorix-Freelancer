-- 000010 — Cross-cutting: append-only audit log, fraud cases, transactional outbox.
-- The `outbox` table exists logically in EACH service database (created by that service's
-- migrations). It is defined here once for the shared/admin DB; copy into per-service schema.
BEGIN;

-- ── audit_logs: append-only, partitioned monthly, archived to S3 (Object Lock) ──
CREATE TABLE audit_logs (
    id              BIGINT GENERATED ALWAYS AS IDENTITY,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id        UUID,                     -- NULL for system actions
    actor_type      TEXT NOT NULL DEFAULT 'user', -- user|admin|system|service
    action          TEXT NOT NULL,            -- 'screenshot.view','user.suspend','payment.refund'
    resource_type   TEXT NOT NULL,
    resource_id     TEXT,
    -- Who/where: helps investigations and impossible-travel detection.
    ip              INET,
    user_agent      TEXT,
    -- Before/after or context payload (PII-minimized).
    context         JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Tamper-evidence: hash chain — each row includes the prev row's hash.
    prev_hash       BYTEA,
    row_hash        BYTEA,
    PRIMARY KEY (id, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE INDEX idx_audit_actor ON audit_logs (actor_id, occurred_at DESC);
CREATE INDEX idx_audit_resource ON audit_logs (resource_type, resource_id, occurred_at DESC);
CREATE INDEX idx_audit_action ON audit_logs (action, occurred_at DESC);
SELECT ensure_month_partition('audit_logs', date_trunc('month', now())::date);
SELECT ensure_month_partition('audit_logs', (date_trunc('month', now()) + interval '1 month')::date);

-- ── Fraud (owned by `fraud`) ────────────────────────────────────────────────
CREATE TABLE fraud_cases (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_type    TEXT NOT NULL,            -- 'user'|'contract'|'screenshot'|'payment'
    subject_id      UUID NOT NULL,
    risk_score      NUMERIC(4,3) NOT NULL,
    status          fraud_case_status NOT NULL DEFAULT 'open',
    reason_codes    TEXT[] NOT NULL DEFAULT '{}',
    assigned_to     UUID REFERENCES users(id),
    resolution      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);
CREATE INDEX idx_fraud_status ON fraud_cases (status, risk_score DESC);
CREATE INDEX idx_fraud_subject ON fraud_cases (subject_type, subject_id);
CREATE TRIGGER trg_fraud_updated BEFORE UPDATE ON fraud_cases
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE fraud_signals (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subject_type    TEXT NOT NULL,
    subject_id      UUID NOT NULL,
    signal          TEXT NOT NULL,            -- 'duplicate_screenshot','vm_detected','impossible_activity'
    weight          NUMERIC(4,3) NOT NULL,
    details         JSONB NOT NULL DEFAULT '{}'::jsonb,
    observed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_signals_subject ON fraud_signals (subject_type, subject_id, observed_at DESC);

CREATE TABLE risk_scores (
    subject_type    TEXT NOT NULL,
    subject_id      UUID NOT NULL,
    score           NUMERIC(4,3) NOT NULL,
    band            TEXT NOT NULL,            -- low|medium|high|critical
    features        JSONB NOT NULL DEFAULT '{}'::jsonb,
    model_version   TEXT,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (subject_type, subject_id)
);

CREATE TABLE admin_actions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id        UUID NOT NULL REFERENCES users(id),
    action          TEXT NOT NULL,
    target_type     TEXT NOT NULL,
    target_id       TEXT,
    reason          TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_admin_actions_admin ON admin_actions (admin_id, created_at DESC);

-- ── Transactional outbox (one per service DB; relay publishes to Kafka) ──────
CREATE TABLE outbox (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    aggregate_type  TEXT NOT NULL,            -- 'contract','payment',...
    aggregate_id    TEXT NOT NULL,
    event_type      TEXT NOT NULL,            -- 'contract.activated'
    topic           TEXT NOT NULL,
    partition_key   TEXT NOT NULL,
    payload         JSONB NOT NULL,
    headers         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);
-- Relay polls unpublished rows in order; index supports that scan.
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Idempotency for consumers (dedupe replays).
CREATE TABLE processed_events (
    consumer        TEXT NOT NULL,
    event_id        TEXT NOT NULL,
    processed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

COMMIT;
