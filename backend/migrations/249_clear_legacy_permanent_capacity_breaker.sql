-- 249_clear_legacy_permanent_capacity_breaker.sql
-- 清理历史 OpenAI 容量熔断中残留的永久停调标记（方案 B：只清标记，不改 schedulable）
--
-- 归因说明：
-- 旧版容量熔断（Task #142 / #207）在触发 Level 4 永久停调时执行了：
--   UPDATE accounts SET extra = $1::jsonb, schedulable = FALSE, updated_at = NOW() WHERE id = $2;
-- 其中 extraJSON 中保存了 {"openai_capacity_breaker": {"models": {..., "permanent": true, "reason": "server_is_overloaded"}}}。
--
-- 迁移无法区分 schedulable=false 是熔断置的还是管理员后来人工停用的，因此：
-- 1. 只清除 extra 中的 openai_capacity_breaker 永久短路标记，并记录审计标记 (extra->'capacity_breaker_recovered')；
-- 2. 不修改 schedulable，需要恢复调度的账号由管理员手动启用；
-- 3. 向 scheduler_outbox 投递变更事件，确保运行时调度缓存与快照同步失效更新。

WITH cleared_accounts AS (
    UPDATE accounts
    SET extra = (extra - 'openai_capacity_breaker') || jsonb_build_object(
            'capacity_breaker_recovered', jsonb_build_object(
                'recovered_at', NOW(),
                'reason', 'legacy_permanent_capacity_breaker_cleared'
            )
        ),
        updated_at = NOW()
    WHERE deleted_at IS NULL
      AND platform = 'openai'
      AND extra->'openai_capacity_breaker'->'models' IS NOT NULL
      AND EXISTS (
        SELECT 1
        FROM jsonb_each(extra->'openai_capacity_breaker'->'models') AS m(model_key, model_val)
        WHERE (model_val->>'permanent')::boolean IS TRUE
      )
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, created_at)
SELECT 'account_changed', id, NOW()
FROM cleared_accounts;
