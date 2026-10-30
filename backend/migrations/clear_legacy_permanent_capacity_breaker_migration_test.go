package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClearLegacyPermanentCapacityBreakerMigration 校验 249 号迁移脚本：
// 1. 严格归因针对 platform='openai' 且由旧版容量熔断导致的 schedulable=false 账号（有 permanent=true 与 server_is_overloaded 签名）；
// 2. 注入 capacity_breaker_recovered 审计字段并自动将变更事件投递至 scheduler_outbox 刷新调度快照；
// 3. 清理残留的 openai_capacity_breaker 永久短路状态。
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
	require.Contains(t, combined, "WITH recovered_accounts AS (")
	require.Contains(t, combined, "platform = 'openai'")
	require.Contains(t, combined, "schedulable = FALSE")
	require.Contains(t, combined, "capacity_breaker_recovered")
	require.Contains(t, combined, "INSERT INTO scheduler_outbox")
	require.Contains(t, combined, "(model_val->>'permanent')::boolean IS TRUE")
	require.Contains(t, combined, "model_val->>'reason' = 'server_is_overloaded'")
	require.Contains(t, combined, "extra - 'openai_capacity_breaker'")
}
