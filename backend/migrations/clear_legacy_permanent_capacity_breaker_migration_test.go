package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClearLegacyPermanentCapacityBreakerMigration 校验 249 号迁移脚本：
// 1. 只针对 platform='openai' 且带旧版永久容量熔断标记（permanent=true）的账号；
// 2. 注入 capacity_breaker_recovered 审计字段并自动将变更事件投递至 scheduler_outbox 刷新调度快照；
// 3. 清理残留的 openai_capacity_breaker 永久短路状态，不改写 schedulable（保留管理员人工停用）。
func TestClearLegacyPermanentCapacityBreakerMigration(t *testing.T) {
	content, err := FS.ReadFile("249_clear_legacy_permanent_capacity_breaker.sql")
	require.NoError(t, err)

	var stmts []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		stmts = append(stmts, trimmed)
	}
	combined := strings.Join(stmts, " ")
	require.Contains(t, combined, "WITH cleared_accounts AS (")
	require.Contains(t, combined, "platform = 'openai'")
	require.Contains(t, combined, "capacity_breaker_recovered")
	require.Contains(t, combined, "INSERT INTO scheduler_outbox")
	require.Contains(t, combined, "(model_val->>'permanent')::boolean IS TRUE")
	require.Contains(t, combined, "extra - 'openai_capacity_breaker'")
	// 迁移分不清人工停用与熔断停用，绝不能改写 schedulable。
	require.NotContains(t, strings.ToLower(combined), "schedulable")
}
