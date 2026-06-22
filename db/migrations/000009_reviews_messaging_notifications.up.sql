-- 000009 — Reviews (`review`), messaging (`messaging`), notifications (`notification`).
BEGIN;

-- ── Reviews ─────────────────────────────────────────────────────────────────
CREATE TABLE reviews (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL REFERENCES contracts(id) ON DELETE CASCADE,
    reviewer_id     UUID NOT NULL REFERENCES users(id),
    reviewee_id     UUID NOT NULL REFERENCES users(id),
    -- Overall 1..5; dimension scores in JSONB (quality, communication, deadlines, ...).
    rating          SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    dimensions      JSONB NOT NULL DEFAULT '{}'::jsonb,
    comment         TEXT,
    -- Reviews are hidden until both sides submit or the window closes (double-blind).
    is_published    BOOLEAN NOT NULL DEFAULT false,
    published_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (contract_id, reviewer_id)
);
CREATE INDEX idx_reviews_reviewee ON reviews (reviewee_id) WHERE is_published;

CREATE TABLE review_responses (
    review_id   UUID PRIMARY KEY REFERENCES reviews(id) ON DELETE CASCADE,
    responder_id UUID NOT NULL REFERENCES users(id),
    response    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE reputation_scores (
    user_id     UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    score       NUMERIC(6,2) NOT NULL DEFAULT 0,
    job_success_pct SMALLINT,
    recompute_at TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Messaging ───────────────────────────────────────────────────────────────
CREATE TABLE conversations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    contract_id     UUID REFERENCES contracts(id),
    project_id      UUID REFERENCES projects(id),
    subject         TEXT,
    last_message_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE conversation_participants (
    conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role            TEXT NOT NULL DEFAULT 'member',
    last_read_at    TIMESTAMPTZ,
    muted           BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (conversation_id, user_id)
);

-- Messages: high volume, partitioned monthly by created_at.
CREATE TABLE messages (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    conversation_id UUID NOT NULL,
    sender_id       UUID NOT NULL,
    body            TEXT,
    -- Optional structured payload (system messages, contract events surfaced in chat).
    kind            TEXT NOT NULL DEFAULT 'text', -- text|file|system
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at       TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE INDEX idx_messages_conv ON messages (conversation_id, created_at DESC);
SELECT ensure_month_partition('messages', date_trunc('month', now())::date);
SELECT ensure_month_partition('messages', (date_trunc('month', now()) + interval '1 month')::date);

CREATE TABLE message_attachments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id  UUID NOT NULL,
    s3_key      TEXT NOT NULL,
    filename    TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    content_type TEXT,
    -- Antivirus scan result (ClamAV/VirusTotal) before download is allowed.
    scan_status TEXT NOT NULL DEFAULT 'pending', -- pending|clean|infected
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_attachments_message ON message_attachments (message_id);

-- ── Notifications ───────────────────────────────────────────────────────────
CREATE TABLE notification_preferences (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,               -- 'proposal.submitted', 'milestone.approved', ...
    channel     notification_channel NOT NULL,
    enabled     BOOLEAN NOT NULL DEFAULT true,
    PRIMARY KEY (user_id, event_type, channel)
);

CREATE TABLE notifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type            TEXT NOT NULL,
    title           TEXT NOT NULL,
    body            TEXT,
    data            JSONB NOT NULL DEFAULT '{}'::jsonb,
    read_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notifications_user_unread ON notifications (user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE TABLE delivery_attempts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    notification_id UUID NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    channel         notification_channel NOT NULL,
    status          TEXT NOT NULL DEFAULT 'queued', -- queued|sent|delivered|failed|bounced
    provider_ref    TEXT,
    error           TEXT,
    attempted_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_delivery_notification ON delivery_attempts (notification_id);

COMMIT;
