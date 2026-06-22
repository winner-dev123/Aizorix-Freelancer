-- 000001 — Extensions, enum types, and shared helper functions.
-- Conventions used across the schema:
--   * UUID v4 primary keys via pgcrypto's gen_random_uuid() (swap for uuidv7 when available).
--   * timestamptz everywhere; created_at/updated_at maintained by trigger.
--   * Soft delete via deleted_at IS NULL semantics (partial indexes exclude deleted rows).
--   * Money stored as BIGINT minor units (cents) + currency code — never floats.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;     -- gen_random_uuid(), digest()
CREATE EXTENSION IF NOT EXISTS citext;       -- case-insensitive email
CREATE EXTENSION IF NOT EXISTS pg_trgm;      -- fuzzy text indexes
CREATE EXTENSION IF NOT EXISTS btree_gist;   -- exclusion constraints (e.g. no overlapping sessions)

-- ── Enum types ──────────────────────────────────────────────────────────────
CREATE TYPE user_status        AS ENUM ('pending', 'active', 'suspended', 'banned', 'deleted');
CREATE TYPE account_type       AS ENUM ('client', 'freelancer', 'admin', 'support');
CREATE TYPE kyc_status         AS ENUM ('not_started', 'pending', 'verified', 'rejected');
CREATE TYPE mfa_type           AS ENUM ('totp', 'webauthn', 'sms', 'backup_code');

CREATE TYPE project_status     AS ENUM ('draft', 'published', 'in_progress', 'closed', 'archived', 'flagged');
CREATE TYPE budget_type        AS ENUM ('fixed', 'hourly');
CREATE TYPE experience_level   AS ENUM ('entry', 'intermediate', 'expert');

CREATE TYPE proposal_status    AS ENUM ('submitted', 'shortlisted', 'accepted', 'declined', 'withdrawn');

CREATE TYPE contract_status    AS ENUM ('pending_funding', 'active', 'paused', 'completed', 'cancelled', 'disputed');
CREATE TYPE milestone_status   AS ENUM ('pending', 'funded', 'submitted', 'approved', 'revision_requested', 'released', 'refunded');

CREATE TYPE worksession_status AS ENUM ('open', 'closed', 'discarded');

CREATE TYPE screenshot_status  AS ENUM ('pending_upload', 'stored', 'integrity_failed', 'flagged', 'deleted');

CREATE TYPE escrow_status      AS ENUM ('held', 'partially_released', 'released', 'refunded');
CREATE TYPE payment_status     AS ENUM ('requires_action', 'processing', 'succeeded', 'failed', 'refunded', 'disputed');
CREATE TYPE txn_type           AS ENUM ('deposit', 'escrow_hold', 'escrow_release', 'fee', 'payout', 'refund', 'chargeback', 'adjustment');
CREATE TYPE withdrawal_status  AS ENUM ('requested', 'processing', 'paid', 'failed', 'cancelled');

CREATE TYPE dispute_status     AS ENUM ('open', 'in_review', 'resolved_client', 'resolved_freelancer', 'split', 'cancelled');
CREATE TYPE fraud_case_status  AS ENUM ('open', 'investigating', 'confirmed', 'dismissed');
CREATE TYPE notification_channel AS ENUM ('in_app', 'email', 'push', 'sms');

-- ── Helper: maintain updated_at ─────────────────────────────────────────────
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ── Helper: create monthly range partitions for a parent table ──────────────
-- In production pg_partman or a scheduled job pre-creates partitions; this helper
-- is used by migrations/tests to materialize a partition for a given month.
CREATE OR REPLACE FUNCTION ensure_month_partition(parent regclass, month date)
RETURNS void AS $$
DECLARE
    start_ts date := date_trunc('month', month);
    end_ts   date := (date_trunc('month', month) + interval '1 month');
    part_name text := format('%s_%s', parent::text, to_char(start_ts, 'YYYY_MM'));
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF %s FOR VALUES FROM (%L) TO (%L)',
            part_name, parent::text, start_ts, end_ts);
    END IF;
END;
$$ LANGUAGE plpgsql;

COMMIT;
