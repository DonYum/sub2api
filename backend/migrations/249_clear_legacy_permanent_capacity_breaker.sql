-- 249_clear_legacy_permanent_capacity_breaker.sql
-- 清理历史 OpenAI 容量熔断中残留的永久停调状态（方案 A：取消永久停调，自愈恢复）
--
-- 归因安全说明：
-- 旧版容量熔断（Task #142 / #207）在触发 Level 4 永久停调时执行了：
--   UPDATE accounts SET extra = $1::jsonb, schedulable = FALSE, updated_at = NOW() WHERE id = $2;
-- 其中 extraJSON 中保存了 {"openai_capacity_breaker": {"models": {..., "permanent": true, "reason": "server_is_overloaded"}}};
-- 并且在 decision.Until 为 nil 时，未记录 model_rate_limits。
--
-- 本迁移做保守安全处理：
-- 1. 针对平台为 openai 且 extra 中含有永久容量熔断标记 (permanent=true, reason='server_is_overloaded') 的账号：
--    - 清除 extra 中的 openai_capacity_breaker 永久短路标记；
--    - 如果 schedulable = false，在 extra 中记录恢复审计标记 (extra->'capacity_breaker_recovered') 并置 schedulable = true；
--    - 绝不触碰非 openai 平台或由管理员人工主动停用（无 permanent 容量熔断签名）的账号；
-- 2. 向 scheduler_outbox 投递变更事件，确保运行时调度缓存与快照同步失效更新。

WITH recovered_accounts AS (
    UPDATE accounts
    SET schedulable = TRUE,
        extra = (extra - 'openai_capacity_breaker') || jsonb_build_object(
            'capacity_breaker_recovered', jsonb_build_object(
                'recovered_at', NOW(),
                'reason', 'legacy_permanent_capacity_breaker_cleared'
            )
        ),
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
            AND model_val->>'reason' = 'server_is_overloaded'
        )
      )
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, created_at)
SELECT 'account_changed', id, NOW()
FROM recovered_accounts;

-- 对于仍然处于调度状态 (schedulable=true) 但 extra 中残留历史 permanent 标记的账号，清理该熔断记录
UPDATE accounts
SET extra = extra - 'openai_capacity_breaker',
    updated_at = NOW()
WHERE deleted_at IS NULL
  AND platform = 'openai'
  AND schedulable = TRUE
  AND extra ? 'openai_capacity_breaker'
  AND (
    extra->'openai_capacity_breaker'->'models' IS NOT NULL
    AND EXISTS (
      SELECT 1
      FROM jsonb_each(extra->'openai_capacity_breaker'->'models') AS m(model_key, model_val)
      WHERE (model_val->>'permanent')::boolean IS TRUE
    )
  );
