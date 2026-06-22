-- 000005 — Contracts, milestones, hourly terms, deliverables, disputes (owned by `contract`).
BEGIN;

CREATE TABLE contracts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    proposal_id     UUID NOT NULL REFERENCES proposals(id),
    client_id       UUID NOT NULL REFERENCES users(id),
    freelancer_id   UUID NOT NULL REFERENCES users(id),
    budget_type     budget_type NOT NULL,
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    -- Fixed: total contract value. Hourly: NULL (driven by weekly billing).
    total_amount_cents BIGINT,
    -- Hourly terms:
    hourly_rate_cents  BIGINT,
    weekly_hour_limit  INT,
    status          contract_status NOT NULL DEFAULT 'pending_funding',
    -- Platform fee in basis points applied to freelancer earnings.
    platform_fee_bps INT NOT NULL DEFAULT 1000,  -- 10%
    started_at      TIMESTAMPTZ,
    ended_at        TIMESTAMPTZ,
    end_reason      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT parties_distinct CHECK (client_id <> freelancer_id),
    CONSTRAINT hourly_terms CHECK (
        budget_type <> 'hourly' OR (hourly_rate_cents IS NOT NULL))
);
CREATE INDEX idx_contracts_client ON contracts (client_id, status);
CREATE INDEX idx_contracts_freelancer ON contracts (freelancer_id, status);
CREATE INDEX idx_contracts_project ON contracts (project_id);
CREATE TRIGGER trg_contracts_updated BEFORE UPDATE ON contracts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Event-sourced state transitions for the contract state machine (audit + replay).
CREATE TABLE contract_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    contract_id UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    from_status contract_status,
    to_status   contract_status NOT NULL,
    event       TEXT NOT NULL,                -- 'fund','activate','submit_milestone',...
    actor_id    UUID,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_contract_events_contract ON contract_events (contract_id, id);

-- ── Fixed-price milestones ──────────────────────────────────────────────────
CREATE TABLE milestones (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    seq             INT NOT NULL,
    title           TEXT NOT NULL,
    description     TEXT,
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    status          milestone_status NOT NULL DEFAULT 'pending',
    due_at          TIMESTAMPTZ,
    funded_at       TIMESTAMPTZ,
    submitted_at    TIMESTAMPTZ,
    approved_at     TIMESTAMPTZ,
    released_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (contract_id, seq)
);
CREATE INDEX idx_milestones_contract ON milestones (contract_id, status);
CREATE TRIGGER trg_milestones_updated BEFORE UPDATE ON milestones
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE deliverables (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    milestone_id UUID NOT NULL REFERENCES milestones(id) ON DELETE CASCADE,
    submitted_by UUID NOT NULL REFERENCES users(id),
    note        TEXT,
    s3_keys     TEXT[] NOT NULL DEFAULT '{}',
    revision    INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_deliverables_milestone ON deliverables (milestone_id);

-- ── Hourly contract config (1:1 with hourly contracts) ──────────────────────
CREATE TABLE hourly_contracts (
    contract_id        UUID PRIMARY KEY REFERENCES contracts(id) ON DELETE CASCADE,
    -- Billing week starts on this weekday (0=Sunday) in the freelancer's timezone.
    billing_week_start SMALLINT NOT NULL DEFAULT 1,
    -- Whether manual (non-tracked) time is allowed and auto-billed.
    allow_manual_time  BOOLEAN NOT NULL DEFAULT false,
    -- Minimum activity % below which hours are auto-flagged for review.
    min_activity_pct   SMALLINT NOT NULL DEFAULT 25,
    require_screenshots BOOLEAN NOT NULL DEFAULT true,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Disputes (raised against a contract; owned by `contract`, worked by `admin`) ──
CREATE TABLE disputes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    milestone_id    UUID REFERENCES milestones(id),
    raised_by       UUID NOT NULL REFERENCES users(id),
    against         UUID NOT NULL REFERENCES users(id),
    reason          TEXT NOT NULL,
    amount_cents    BIGINT,
    status          dispute_status NOT NULL DEFAULT 'open',
    resolution_note TEXT,
    assigned_admin  UUID REFERENCES users(id),
    resolved_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_disputes_status ON disputes (status, created_at);
CREATE INDEX idx_disputes_contract ON disputes (contract_id);
CREATE TRIGGER trg_disputes_updated BEFORE UPDATE ON disputes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMIT;
