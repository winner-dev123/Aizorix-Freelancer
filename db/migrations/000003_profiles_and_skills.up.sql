-- 000003 — Freelancer/client profiles, skills, portfolio, KYC (owned by `user`).
BEGIN;

CREATE TABLE skills (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,         -- 'golang', 'react', 'figma'
    name        TEXT NOT NULL,
    category    TEXT NOT NULL,                -- 'engineering','design','writing',...
    aliases     TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_skills_name_trgm ON skills USING gin (name gin_trgm_ops);
CREATE INDEX idx_skills_category ON skills (category);

CREATE TABLE freelancer_profiles (
    user_id           UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    headline          TEXT,
    bio               TEXT,
    hourly_rate_cents BIGINT,                 -- nullable until set
    currency          CHAR(3) NOT NULL DEFAULT 'USD',
    experience        experience_level NOT NULL DEFAULT 'intermediate',
    availability_hours_per_week INT,
    timezone          TEXT,
    country           CHAR(2),
    -- PII fields are envelope-encrypted at the application layer; ciphertext stored here.
    legal_name_encrypted BYTEA,
    address_encrypted    BYTEA,
    tax_id_encrypted     BYTEA,
    wrapped_dek          BYTEA,               -- KMS-wrapped data key protecting the above
    kyc_status        kyc_status NOT NULL DEFAULT 'not_started',
    -- Denormalized reputation for fast reads; source of truth in review service.
    rating_avg        NUMERIC(3,2) NOT NULL DEFAULT 0,
    rating_count      INT NOT NULL DEFAULT 0,
    total_earned_cents BIGINT NOT NULL DEFAULT 0,
    jobs_completed    INT NOT NULL DEFAULT 0,
    profile_completeness SMALLINT NOT NULL DEFAULT 0,  -- 0..100
    is_searchable     BOOLEAN NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_freelancer_updated BEFORE UPDATE ON freelancer_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX idx_freelancer_searchable ON freelancer_profiles (is_searchable) WHERE is_searchable;

CREATE TABLE client_profiles (
    user_id           UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    company_name      TEXT,
    website           TEXT,
    industry          TEXT,
    company_size      TEXT,
    country           CHAR(2),
    billing_address_encrypted BYTEA,
    tax_id_encrypted          BYTEA,
    wrapped_dek               BYTEA,
    payment_verified  BOOLEAN NOT NULL DEFAULT false,
    total_spent_cents BIGINT NOT NULL DEFAULT 0,
    hires_count       INT NOT NULL DEFAULT 0,
    rating_avg        NUMERIC(3,2) NOT NULL DEFAULT 0,
    rating_count      INT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER trg_client_updated BEFORE UPDATE ON client_profiles
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE freelancer_skills (
    user_id     UUID NOT NULL REFERENCES freelancer_profiles(user_id) ON DELETE CASCADE,
    skill_id    UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    level       experience_level NOT NULL DEFAULT 'intermediate',
    years       NUMERIC(4,1),
    PRIMARY KEY (user_id, skill_id)
);
CREATE INDEX idx_freelancer_skills_skill ON freelancer_skills (skill_id);

CREATE TABLE portfolio_items (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES freelancer_profiles(user_id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT,
    url         TEXT,
    image_keys  TEXT[] NOT NULL DEFAULT '{}', -- S3 keys
    skills      UUID[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_portfolio_user ON portfolio_items (user_id) WHERE deleted_at IS NULL;

CREATE TABLE kyc_records (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL DEFAULT 'sumsub',
    provider_ref    TEXT,
    status          kyc_status NOT NULL DEFAULT 'pending',
    -- Decision payload from KYC provider (no raw documents stored here).
    result          JSONB NOT NULL DEFAULT '{}'::jsonb,
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_kyc_user ON kyc_records (user_id);

COMMIT;
