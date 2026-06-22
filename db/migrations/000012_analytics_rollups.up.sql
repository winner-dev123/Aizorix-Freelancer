-- 000012 — Analytics rollup tables (owned by `analytics`). These OLAP aggregates are
-- maintained by the analytics consumer as it ingests the event stream. Kept in the same
-- database for the scaffold; a production deployment lands these in a columnar store
-- (Redshift/ClickHouse) fed by the same consumer.
BEGIN;

-- Per-day count of every event type seen on the bus.
CREATE TABLE IF NOT EXISTS event_counts (
    day        DATE   NOT NULL,
    event_type TEXT   NOT NULL,
    count      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, event_type)
);

-- Gross merchandise value per day + currency, split into platform fee and net to freelancers,
-- plus a distinct-contract counter (bumped only on contract.activated).
CREATE TABLE IF NOT EXISTS gmv_daily (
    day         DATE    NOT NULL,
    currency    CHAR(3) NOT NULL,
    gross_cents BIGINT  NOT NULL DEFAULT 0,
    fee_cents   BIGINT  NOT NULL DEFAULT 0,
    net_cents   BIGINT  NOT NULL DEFAULT 0,
    contracts   BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (day, currency)
);

-- Marketplace funnel per day: published -> proposed -> activated.
CREATE TABLE IF NOT EXISTS funnel_daily (
    day                 DATE   PRIMARY KEY,
    projects_published  BIGINT NOT NULL DEFAULT 0,
    proposals_submitted BIGINT NOT NULL DEFAULT 0,
    contracts_activated BIGINT NOT NULL DEFAULT 0
);

COMMIT;
