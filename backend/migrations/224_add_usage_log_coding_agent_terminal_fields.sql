-- Persist coding-agent terminal/client observability fields on each usage log.
-- These nullable columns are append-only request metadata; no prompt, messages,
-- tool payloads, response text, or raw API keys are stored here.

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_machine_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_machine_source VARCHAR(32);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_device_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_account_uuid VARCHAR(128);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_originator VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS codex_installation_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS codex_window_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS codex_session_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS codex_thread_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS codex_turn_id VARCHAR(255);
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS terminal_hash VARCHAR(64);

COMMENT ON COLUMN usage_logs.client_machine_id IS 'Coding-agent client machine/installation identifier observed from allowlisted request metadata';
COMMENT ON COLUMN usage_logs.client_machine_source IS 'Source of client_machine_id: x_client_machine, codex_installation, metadata_device, or future allowlisted sources';
COMMENT ON COLUMN usage_logs.client_device_id IS 'Claude Code metadata.user_id device_id component, when provided';
COMMENT ON COLUMN usage_logs.client_account_uuid IS 'Claude Code metadata.user_id account_uuid component, when provided';
COMMENT ON COLUMN usage_logs.client_originator IS 'Originator request header, when provided by the coding agent';
COMMENT ON COLUMN usage_logs.codex_installation_id IS 'Codex installation_id from header or client_metadata';
COMMENT ON COLUMN usage_logs.codex_window_id IS 'Codex window_id from header, client_metadata, or x-codex-turn-metadata';
COMMENT ON COLUMN usage_logs.codex_session_id IS 'Codex session_id from client_metadata, x-codex-turn-metadata, or metadata.user_id';
COMMENT ON COLUMN usage_logs.codex_thread_id IS 'Codex thread_id from client_metadata or x-codex-turn-metadata';
COMMENT ON COLUMN usage_logs.codex_turn_id IS 'Codex turn_id from client_metadata or x-codex-turn-metadata';
COMMENT ON COLUMN usage_logs.terminal_hash IS 'Derived terminal grouping hash for analytics; raw allowlisted fields remain stored in adjacent columns';
