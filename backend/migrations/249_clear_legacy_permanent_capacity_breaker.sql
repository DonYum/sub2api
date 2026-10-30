-- 249_clear_legacy_permanent_capacity_breaker.sql
-- 清理历史 OpenAI 容量熔断中残留的永久停调状态（方案 A：取消永久停调，自愈恢复）
-- 1. 如果 accounts.schedulable 被旧版容量熔断置为 false，且 extra 中记录了 permanent 容量熔断，
--    重置 schedulable = true，避免因历史过载抖动被永久阻断（严格限定带 permanent 熔断标记的账号，不影响普通人工停用账号）；
-- 2. 清理 extra->'openai_capacity_breaker' 中的 permanent 标记及历史状态。

UPDATE accounts
SET schedulable = TRUE,
    extra = extra - 'openai_capacity_breaker',
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND schedulable = FALSE
  AND (
    extra->'openai_capacity_breaker'->'models' IS NOT NULL
    AND EXISTS (
      SELECT 1
      FROM jsonb_each(extra->'openai_capacity_breaker'->'models') AS m(model_key, model_val)
      WHERE (model_val->>'permanent')::boolean IS TRUE
    )
  );

-- 对于仍然 schedulable=true 但 extra->'openai_capacity_breaker' 中含有 permanent=true 记录的，清理该历史熔断状态
UPDATE accounts
SET extra = extra - 'openai_capacity_breaker',
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND extra ? 'openai_capacity_breaker'
  AND (
    extra->'openai_capacity_breaker'->'models' IS NOT NULL
    AND EXISTS (
      SELECT 1
      FROM jsonb_each(extra->'openai_capacity_breaker'->'models') AS m(model_key, model_val)
      WHERE (model_val->>'permanent')::boolean IS TRUE
    )
  );
