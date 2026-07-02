-- EE inbound auth: add the admin privilege flag to the API key registry.
-- Keys with is_admin = TRUE may use the /admin/* management endpoints; ordinary
-- tenant keys (default) are limited to the gateway. Idempotent.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE;
