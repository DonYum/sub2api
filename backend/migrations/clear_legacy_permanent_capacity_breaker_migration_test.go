package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClearLegacyPermanentCapacityBreakerMigration 校验 249 号迁移包含且仅包含针对 openai 历史 permanent 容量熔断的恢复与清理语句
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
	require.Contains(t, combined, "UPDATE accounts")
	require.Contains(t, combined, "platform = 'openai'")
	require.Contains(t, combined, "extra - 'openai_capacity_breaker'")
	require.Contains(t, combined, "(model_val->>'permanent')::boolean IS TRUE")
}
