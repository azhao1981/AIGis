-- AIGis Enterprise Edition — usage metering schema.
-- Licensed under the AIGis Enterprise Edition License (see ee/LICENSE), NOT the
-- AGPLv3 that governs the open-source core.
--
-- PORTABILITY: this file is split so that Timescale is optional and isolated.
--   * The CREATE TABLE below is 100% standard PostgreSQL — it works on any PG.
--   * The single create_hypertable() call is the ONLY Timescale-specific line.
--     To run on vanilla PostgreSQL (or another DB), simply skip that one line;
--     the table, indexes, and all application INSERT/SELECT stay unchanged.
-- The application layer (ee/billing PostgresSink) issues only standard SQL and
-- is unaware of whether this table is a hypertable.

-- ---------------------------------------------------------------------------
-- 1. Standard table (portable — any PostgreSQL)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS usage_events (
    ts                TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant            TEXT        NOT NULL DEFAULT '',
    subject           TEXT        NOT NULL DEFAULT '',
    request_id        TEXT        NOT NULL DEFAULT '',
    route_id          TEXT        NOT NULL DEFAULT '',
    model             TEXT        NOT NULL DEFAULT '',
    prompt_tokens     INTEGER     NOT NULL DEFAULT 0,
    completion_tokens INTEGER     NOT NULL DEFAULT 0,
    total_tokens      INTEGER     NOT NULL DEFAULT 0,
    streamed          BOOLEAN     NOT NULL DEFAULT FALSE,
    success           BOOLEAN     NOT NULL DEFAULT FALSE,
    duration_ms       BIGINT      NOT NULL DEFAULT 0
);

-- Common query axes: per-tenant over time, and per-model over time.
CREATE INDEX IF NOT EXISTS idx_usage_events_tenant_ts ON usage_events (tenant, ts DESC);
CREATE INDEX IF NOT EXISTS idx_usage_events_model_ts  ON usage_events (model, ts DESC);

-- ---------------------------------------------------------------------------
-- 2. Timescale-only: convert to a hypertable (time-partitioned by ts).
--    THIS IS THE ONLY NON-PORTABLE LINE. Skip it on vanilla PostgreSQL.
--    if_not_exists + migrate_data make it safe to re-run and to run against an
--    already-populated table.
-- ---------------------------------------------------------------------------
SELECT create_hypertable('usage_events', 'ts', if_not_exists => TRUE, migrate_data => TRUE);
