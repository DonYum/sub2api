-- Store sanitized, noise-free user inputs extracted at the gateway before
-- upstream forwarding. Keyed by (request_id, api_key_id) to align with
-- usage_logs and deduplicate across account failover retries.

CREATE TABLE IF NOT EXISTS usage_user_inputs (
    id          BIGSERIAL PRIMARY KEY,
    request_id  VARCHAR(64) NOT NULL,
    api_key_id  BIGINT NOT NULL,
    user_id     BIGINT NOT NULL DEFAULT 0,
    model       VARCHAR(100) NOT NULL DEFAULT '',
    user_input  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_user_inputs_request_id_api_key_unique
    ON usage_user_inputs (request_id, api_key_id);

CREATE INDEX IF NOT EXISTS idx_usage_user_inputs_created_at
    ON usage_user_inputs (created_at);
