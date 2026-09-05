CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_api_key_terminal_created
    ON usage_logs (api_key_id, terminal_hash, created_at DESC, id DESC)
    WHERE terminal_hash IS NOT NULL;
