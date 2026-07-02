-- EE SaaS dashboard users: human logins for the admin UI (distinct from the
-- machine-facing api_keys table). A user belongs to one tenant (tenant == user
-- group in this model); is_admin grants /admin/* privileges.
--
-- Portable standard PostgreSQL DDL (no Timescale-specific syntax).
--
-- The password is NEVER stored in plaintext: only a bcrypt hash is kept, so a DB
-- leak does not expose usable credentials. `enabled` allows soft-disable.
CREATE TABLE IF NOT EXISTS users (
    id            BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    password_hash TEXT        NOT NULL,
    tenant        TEXT        NOT NULL,
    is_admin      BOOLEAN     NOT NULL DEFAULT FALSE,
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_tenant ON users (tenant);
