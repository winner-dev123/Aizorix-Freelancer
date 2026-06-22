-- 000007 — Screenshot control plane (owned by `screenshot`). Blobs live in S3;
-- only encrypted-blob *metadata* lives here. Partitioned monthly for retention/archival.
BEGIN;

-- Parent: append-only, range-partitioned by capture time.
CREATE TABLE screenshots (
    id              UUID NOT NULL DEFAULT gen_random_uuid(),
    contract_id     UUID NOT NULL,
    session_id      UUID NOT NULL,
    slice_id        UUID,                     -- links to time_slices.id
    freelancer_id   UUID NOT NULL,
    captured_at     TIMESTAMPTZ NOT NULL,
    -- S3 location of the AES-256-GCM ciphertext object.
    s3_bucket       TEXT NOT NULL,
    s3_key          TEXT NOT NULL,
    status          screenshot_status NOT NULL DEFAULT 'pending_upload',
    -- Retention: hard-delete after this timestamp unless on legal hold.
    retain_until    TIMESTAMPTZ,
    legal_hold      BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, captured_at)
) PARTITION BY RANGE (captured_at);
CREATE INDEX idx_screenshots_contract ON screenshots (contract_id, captured_at);
CREATE INDEX idx_screenshots_session ON screenshots (session_id, captured_at);
CREATE INDEX idx_screenshots_status ON screenshots (status) WHERE status <> 'stored';

SELECT ensure_month_partition('screenshots', date_trunc('month', now())::date);
SELECT ensure_month_partition('screenshots', (date_trunc('month', now()) + interval '1 month')::date);

-- 1:1 metadata: integrity, crypto material, and fraud signals for each screenshot.
CREATE TABLE screenshot_metadata (
    screenshot_id   UUID PRIMARY KEY,
    captured_at     TIMESTAMPTZ NOT NULL,
    -- Integrity: device-computed sha256 of the *ciphertext*; server re-verifies.
    sha256_cipher   BYTEA NOT NULL,
    integrity_verified BOOLEAN NOT NULL DEFAULT false,
    -- Envelope encryption: AES-256-GCM data key, wrapped by KMS (or local master key in dev).
    wrapped_dek     BYTEA NOT NULL,
    gcm_nonce       BYTEA NOT NULL,
    kms_key_id      TEXT,
    -- Image facts:
    width           INT,
    height          INT,
    format          TEXT NOT NULL DEFAULT 'webp',
    size_bytes      BIGINT,
    -- Perceptual hash (pHash) for duplicate / static-screen detection in `fraud`.
    phash           BYTEA,
    -- Ed25519 signature by the device over (sha256_cipher || captured_at || contract_id).
    device_signature BYTEA,
    device_id       UUID,
    -- Fraud flags (set asynchronously by the fraud service).
    is_duplicate    BOOLEAN NOT NULL DEFAULT false,
    is_tampered     BOOLEAN NOT NULL DEFAULT false,
    fraud_score     NUMERIC(4,3),
    flags           JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Audited views: who looked at this screenshot and when (count; detail in audit_logs).
    view_count      INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ssmeta_phash ON screenshot_metadata USING hash (phash);
CREATE INDEX idx_ssmeta_flagged ON screenshot_metadata (fraud_score DESC)
    WHERE is_duplicate OR is_tampered;

COMMIT;
