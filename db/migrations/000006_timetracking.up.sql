-- 000006 — Work sessions, time slices, activity logs, timesheets (owned by `timetracking`).
-- High write volume: activity_logs is RANGE-partitioned by month; partitions are
-- pre-created by a scheduled job (pg_partman/cron) and dropped/archived after retention.
BEGIN;

CREATE TABLE work_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    freelancer_id   UUID NOT NULL REFERENCES users(id),
    device_id       UUID REFERENCES devices(id),
    status          worksession_status NOT NULL DEFAULT 'open',
    started_at      TIMESTAMPTZ NOT NULL,
    ended_at        TIMESTAMPTZ,
    -- Aggregated at close:
    active_seconds  INT NOT NULL DEFAULT 0,
    idle_seconds    INT NOT NULL DEFAULT 0,
    billed_seconds  INT NOT NULL DEFAULT 0,
    avg_activity_pct SMALLINT,                -- 0..100
    memo            TEXT,                     -- freelancer note for the session
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    -- ISO week key (e.g. '2026-W25') for weekly billing grouping.
    billing_week    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ws_contract_week ON work_sessions (contract_id, billing_week);
CREATE INDEX idx_ws_freelancer_open ON work_sessions (freelancer_id) WHERE status = 'open';
CREATE TRIGGER trg_ws_updated BEFORE UPDATE ON work_sessions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- A "slice" is the ~10-minute billing/screenshot unit within a session.
CREATE TABLE time_slices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    contract_id     UUID NOT NULL,
    slice_start     TIMESTAMPTZ NOT NULL,
    slice_end       TIMESTAMPTZ NOT NULL,
    keyboard_events INT NOT NULL DEFAULT 0,
    mouse_events    INT NOT NULL DEFAULT 0,
    active_seconds  INT NOT NULL DEFAULT 0,
    activity_pct    SMALLINT NOT NULL DEFAULT 0,
    active_app      TEXT,
    active_app_title TEXT,
    browser_url_host TEXT,                    -- host only by default for privacy
    -- Linked screenshot (set when screenshot.ingested arrives for this slice).
    screenshot_id   UUID,
    is_manual       BOOLEAN NOT NULL DEFAULT false,
    flagged         BOOLEAN NOT NULL DEFAULT false,
    flag_reason     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, slice_start)
);
CREATE INDEX idx_slices_session ON time_slices (session_id, slice_start);
CREATE INDEX idx_slices_contract ON time_slices (contract_id, slice_start);

-- Raw activity samples (sub-slice granularity), partitioned monthly by time.
CREATE TABLE activity_logs (
    id              BIGINT GENERATED ALWAYS AS IDENTITY,
    contract_id     UUID NOT NULL,
    session_id      UUID NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL,
    keyboard_count  INT NOT NULL DEFAULT 0,
    mouse_count     INT NOT NULL DEFAULT 0,
    mouse_distance_px INT NOT NULL DEFAULT 0,
    is_idle         BOOLEAN NOT NULL DEFAULT false,
    active_app      TEXT,
    PRIMARY KEY (id, sampled_at)
) PARTITION BY RANGE (sampled_at);
CREATE INDEX idx_activity_session ON activity_logs (session_id, sampled_at);

-- Materialize initial partitions (current + next month). A scheduled job extends these.
SELECT ensure_month_partition('activity_logs', date_trunc('month', now())::date);
SELECT ensure_month_partition('activity_logs', (date_trunc('month', now()) + interval '1 month')::date);

-- Weekly timesheet rollup per contract (source for billing.week_ready event).
CREATE TABLE timesheets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    billing_week    TEXT NOT NULL,
    total_seconds   INT NOT NULL DEFAULT 0,
    billable_seconds INT NOT NULL DEFAULT 0,
    amount_cents    BIGINT NOT NULL DEFAULT 0,
    avg_activity_pct SMALLINT,
    status          TEXT NOT NULL DEFAULT 'accumulating', -- accumulating|ready|billed|disputed
    finalized_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (contract_id, billing_week)
);
CREATE TRIGGER trg_timesheets_updated BEFORE UPDATE ON timesheets
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE manual_time_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    freelancer_id   UUID NOT NULL REFERENCES users(id),
    slice_start     TIMESTAMPTZ NOT NULL,
    slice_end       TIMESTAMPTZ NOT NULL,
    reason          TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending', -- pending|approved|rejected
    reviewed_by     UUID REFERENCES users(id),
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_manual_time_contract ON manual_time_requests (contract_id, status);

COMMIT;
