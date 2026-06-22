-- 000004 — Projects & proposals (owned by `project` and `proposal`).
BEGIN;

CREATE TABLE project_categories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id   UUID REFERENCES project_categories(id),
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL
);

CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id     UUID REFERENCES project_categories(id),
    title           TEXT NOT NULL,
    description     TEXT NOT NULL,
    budget_type     budget_type NOT NULL,
    -- Fixed: budget_min/max are the fixed amount. Hourly: hourly rate band.
    budget_min_cents BIGINT,
    budget_max_cents BIGINT,
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    -- Hourly-only: weekly hour cap to bound exposure.
    weekly_hour_limit INT,
    experience_required experience_level,
    estimated_duration_days INT,
    status          project_status NOT NULL DEFAULT 'draft',
    visibility      TEXT NOT NULL DEFAULT 'public', -- 'public' | 'invite_only' | 'private'
    proposals_count INT NOT NULL DEFAULT 0,
    hired_count     INT NOT NULL DEFAULT 0,
    published_at    TIMESTAMPTZ,
    closed_at       TIMESTAMPTZ,
    -- Full-text search vector maintained by trigger (search service also indexes to OpenSearch).
    search_tsv      TSVECTOR,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT budget_range CHECK (budget_max_cents IS NULL OR budget_min_cents IS NULL
                                   OR budget_max_cents >= budget_min_cents)
);
CREATE INDEX idx_projects_client ON projects (client_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_projects_status_pub ON projects (status, published_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_projects_category ON projects (category_id) WHERE status = 'published';
CREATE INDEX idx_projects_search ON projects USING gin (search_tsv);
CREATE TRIGGER trg_projects_updated BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE OR REPLACE FUNCTION projects_tsv() RETURNS trigger AS $$
BEGIN
    NEW.search_tsv :=
        setweight(to_tsvector('english', coalesce(NEW.title,'')), 'A') ||
        setweight(to_tsvector('english', coalesce(NEW.description,'')), 'B');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_projects_tsv BEFORE INSERT OR UPDATE OF title, description ON projects
    FOR EACH ROW EXECUTE FUNCTION projects_tsv();

CREATE TABLE project_skills (
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    skill_id    UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, skill_id)
);
CREATE INDEX idx_project_skills_skill ON project_skills (skill_id);

CREATE TABLE project_attachments (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    s3_key      TEXT NOT NULL,
    filename    TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL,
    content_type TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── proposals ───────────────────────────────────────────────────────────────
CREATE TABLE proposals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    freelancer_id   UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cover_letter    TEXT NOT NULL,
    -- Fixed: total bid. Hourly: proposed hourly rate.
    bid_amount_cents BIGINT NOT NULL,
    currency        CHAR(3) NOT NULL DEFAULT 'USD',
    estimated_duration_days INT,
    status          proposal_status NOT NULL DEFAULT 'submitted',
    -- "Connects" spent (Upwork-style throttling of low-quality bids).
    connects_spent  INT NOT NULL DEFAULT 0,
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at      TIMESTAMPTZ,
    withdrawn_at    TIMESTAMPTZ,
    -- A freelancer may submit at most one active proposal per project.
    UNIQUE (project_id, freelancer_id)
);
CREATE INDEX idx_proposals_project ON proposals (project_id, status);
CREATE INDEX idx_proposals_freelancer ON proposals (freelancer_id, submitted_at DESC);

CREATE TABLE proposal_milestones (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposal_id UUID NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    seq         INT NOT NULL,
    title       TEXT NOT NULL,
    amount_cents BIGINT NOT NULL,
    due_days    INT,
    UNIQUE (proposal_id, seq)
);

CREATE TABLE proposal_answers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    proposal_id UUID NOT NULL REFERENCES proposals(id) ON DELETE CASCADE,
    question    TEXT NOT NULL,
    answer      TEXT NOT NULL
);

COMMIT;
