-- 000002 — Identity, credentials, sessions, MFA, OAuth, and RBAC (Phases 4).
-- Owned by the `auth` and `user` services (RBAC tables read by `user`/`admin`).
BEGIN;

-- ── users (credentials & account state only; profile data lives elsewhere) ──
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           CITEXT NOT NULL,
    phone           TEXT,
    -- Argon2id encoded hash (includes salt + params). NULL for OAuth-only accounts.
    password_hash   TEXT,
    status          user_status NOT NULL DEFAULT 'pending',
    primary_type    account_type NOT NULL DEFAULT 'client',
    email_verified  BOOLEAN NOT NULL DEFAULT false,
    phone_verified  BOOLEAN NOT NULL DEFAULT false,
    mfa_enabled     BOOLEAN NOT NULL DEFAULT false,
    -- ISO country of residence — drives data-residency routing & compliance.
    residency_country CHAR(2),
    locale          TEXT NOT NULL DEFAULT 'en-US',
    last_login_at   TIMESTAMPTZ,
    failed_login_count INT NOT NULL DEFAULT 0,
    locked_until    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);
-- One active account per email (case-insensitive, excludes soft-deleted).
CREATE UNIQUE INDEX uq_users_email_active ON users (email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_users_phone_active ON users (phone) WHERE phone IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX idx_users_status ON users (status) WHERE deleted_at IS NULL;
CREATE TRIGGER trg_users_updated BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── oauth_identities ────────────────────────────────────────────────────────
CREATE TABLE oauth_identities (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider        TEXT NOT NULL,            -- 'google' | 'github' | 'linkedin'
    provider_uid    TEXT NOT NULL,            -- stable subject id from provider
    email           CITEXT,
    raw_profile     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_uid)
);
CREATE INDEX idx_oauth_user ON oauth_identities (user_id);

-- ── devices (for refresh-token binding, fingerprinting, the tracker) ────────
CREATE TABLE devices (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint     TEXT NOT NULL,            -- hashed UA + entropy, or tracker attestation key id
    kind            TEXT NOT NULL DEFAULT 'browser', -- 'browser' | 'desktop_tracker' | 'mobile'
    display_name    TEXT,
    -- Public key the desktop tracker uses to sign screenshot metadata (Ed25519).
    attestation_pubkey BYTEA,
    last_seen_at    TIMESTAMPTZ,
    last_ip         INET,
    trusted         BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, fingerprint)
);

-- ── sessions ────────────────────────────────────────────────────────────────
CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id       UUID REFERENCES devices(id) ON DELETE SET NULL,
    ip              INET,
    user_agent      TEXT,
    -- Session "family" groups a refresh-token rotation chain for reuse detection.
    family_id       UUID NOT NULL DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ,
    revoked_reason  TEXT
);
CREATE INDEX idx_sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_family ON sessions (family_id);

-- ── refresh_tokens (opaque, only the hash is stored; rotation chain) ────────
CREATE TABLE refresh_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- SHA-256 of the random token; the raw token is returned to the client once.
    token_hash      BYTEA NOT NULL,
    -- Points to the token this one replaced; reuse of a rotated token => breach.
    replaced_by     UUID REFERENCES refresh_tokens(id),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX uq_refresh_token_hash ON refresh_tokens (token_hash);
CREATE INDEX idx_refresh_session ON refresh_tokens (session_id);

-- ── mfa_factors ─────────────────────────────────────────────────────────────
CREATE TABLE mfa_factors (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type            mfa_type NOT NULL,
    -- TOTP secret / WebAuthn credential — envelope-encrypted (KMS-wrapped DEK).
    secret_encrypted BYTEA,
    wrapped_dek     BYTEA,
    label           TEXT,
    confirmed_at    TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_mfa_user ON mfa_factors (user_id);

-- ── short-lived verification & reset tokens ────────────────────────────────
CREATE TABLE email_verifications (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE password_resets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── RBAC: roles, permissions, and their assignments ─────────────────────────
CREATE TABLE roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,         -- 'client','freelancer','admin','support','finance_admin'
    description TEXT,
    is_system   BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Verb:resource form, e.g. 'screenshot:read', 'payment:refund', 'user:suspend'.
    code        TEXT NOT NULL UNIQUE,
    description TEXT
);
CREATE TABLE role_permissions (
    role_id        UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id  UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);
CREATE TABLE user_roles (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    -- Optional scoping: a role granted only within one contract/org context.
    scope       TEXT,
    granted_by  UUID REFERENCES users(id),
    granted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id, scope)
);

-- Seed system roles & a baseline permission set.
INSERT INTO roles (name, description, is_system) VALUES
    ('client', 'Posts projects and hires freelancers', true),
    ('freelancer', 'Bids on and performs work', true),
    ('support', 'Customer support staff', true),
    ('admin', 'Platform administrator', true),
    ('finance_admin', 'Payment/escrow privileged operations', true);

INSERT INTO permissions (code, description) VALUES
    ('project:create','Create projects'),
    ('proposal:submit','Submit proposals'),
    ('contract:manage','Manage contracts'),
    ('screenshot:read','View screenshots for own contracts'),
    ('screenshot:audit','Audit any screenshot'),
    ('payment:refund','Issue refunds'),
    ('payment:review','Review payments/disputes'),
    ('user:suspend','Suspend or ban users'),
    ('fraud:review','Review fraud cases'),
    ('audit:read','Read audit logs');

COMMIT;
