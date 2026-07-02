-- EE inbound auth: API key change audit trail (compliance).
-- Records who created/revoked which key and when. Portable standard PostgreSQL
-- DDL (no Timescale-specific syntax).
--
-- Only the SHA-256 key_hash is stored (same as api_keys) — the raw key never
-- touches the DB. actor_* identify the admin principal that made the change.
CREATE TABLE IF NOT EXISTS api_key_audit (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
    action        TEXT        NOT NULL,             -- 'create' | 'revoke'
    key_hash      TEXT        NOT NULL,             -- target key (hash only)
    target_tenant TEXT        NOT NULL DEFAULT '',
    target_admin  BOOLEAN     NOT NULL DEFAULT FALSE,
    actor_subject TEXT        NOT NULL DEFAULT '',  -- who made the change
    actor_tenant  TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_api_key_audit_ts ON api_key_audit (ts DESC);
CREATE INDEX IF NOT EXISTS idx_api_key_audit_key_hash ON api_key_audit (key_hash);
