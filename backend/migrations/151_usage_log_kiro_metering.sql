ALTER TABLE usage_logs
  ADD COLUMN IF NOT EXISTS upstream_kiro_credits NUMERIC,
  ADD COLUMN IF NOT EXISTS upstream_kiro_input_tokens INTEGER,
  ADD COLUMN IF NOT EXISTS upstream_kiro_output_tokens INTEGER;

COMMENT ON COLUMN usage_logs.upstream_kiro_credits IS 'Kiro meteringEvent.usage credits reported by upstream; observability only, not used for billing.';
COMMENT ON COLUMN usage_logs.upstream_kiro_input_tokens IS 'Kiro meteringEvent inputTokens reported by upstream; observability only.';
COMMENT ON COLUMN usage_logs.upstream_kiro_output_tokens IS 'Kiro meteringEvent outputTokens reported by upstream; observability only.';
