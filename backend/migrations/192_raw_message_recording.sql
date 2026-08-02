-- Per-key opt-in raw gateway message capture. Large bodies live in a configured
-- file store; PostgreSQL retains only searchable metadata and relative paths.
ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS raw_message_recording_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS raw_message_records (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(255) NOT NULL,
    -- Logical identifiers, deliberately not foreign keys: deleting an API key
    -- or user must not become blocked by retained capture metadata, and body
    -- files are removed only through the explicit raw-message cleanup path.
    api_key_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    method VARCHAR(16) NOT NULL,
    endpoint VARCHAR(512) NOT NULL,
    status_code INTEGER NOT NULL,
    content_type VARCHAR(255),
    relative_path VARCHAR(1024) NOT NULL UNIQUE,
    request_bytes BIGINT NOT NULL DEFAULT 0,
    response_bytes BIGINT NOT NULL DEFAULT 0,
    request_stored_bytes BIGINT NOT NULL DEFAULT 0,
    response_stored_bytes BIGINT NOT NULL DEFAULT 0,
    request_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    response_truncated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_raw_message_records_api_key_created
    ON raw_message_records(api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_raw_message_records_request_key
    ON raw_message_records(request_id, api_key_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_raw_message_records_created_at
    ON raw_message_records(created_at DESC);
