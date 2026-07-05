-- EE SaaS email tokens: one-time tokens for email verification (activate a
-- freshly registered account) and password reset. A single table serves both,
-- distinguished by `purpose`. Tokens are opaque random strings (128 bits) and
-- are deleted on use, so each is single-use; `expires_at` bounds their lifetime.
--
-- Portable standard PostgreSQL DDL (no Timescale-specific syntax).
--
-- The token itself is the secret (like a session id): treat it as sensitive,
-- never log it. A row is removed the moment it is consumed or once expired.
CREATE TABLE IF NOT EXISTS email_tokens (
    token      TEXT        NOT NULL PRIMARY KEY,
    email      TEXT        NOT NULL,
    purpose    TEXT        NOT NULL,   -- 'verify' | 'reset'
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Look up / clean up a user's outstanding tokens by email+purpose (e.g. issuing
-- a fresh reset token supersedes older ones).
CREATE INDEX IF NOT EXISTS idx_email_tokens_email_purpose
    ON email_tokens (email, purpose);

-- Sweep expired tokens efficiently.
CREATE INDEX IF NOT EXISTS idx_email_tokens_expires_at
    ON email_tokens (expires_at);
