-- EE inbound auth: API key -> tenant registry.
-- Portable standard PostgreSQL DDL (no Timescale-specific syntax).
--
-- The raw API key is NEVER stored: we keep only a SHA-256 hex digest, so a DB
-- leak does not expose usable credentials. `subject` is a human label for the
-- key (who/what it belongs to); `enabled` allows soft-revocation.
CREATE TABLE IF NOT EXISTS api_keys (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key_hash    TEXT        NOT NULL UNIQUE,
    tenant      TEXT        NOT NULL,
    subject     TEXT        NOT NULL DEFAULT '',
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys (tenant);
