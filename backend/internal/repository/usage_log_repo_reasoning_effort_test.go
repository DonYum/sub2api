package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendUsageLogReasoningEffortWhereCondition(t *testing.T) {
	conditions, args := appendUsageLogReasoningEffortWhereConditionWithAlias(
		[]string{"user_id = $1"},
		[]any{int64(7)},
		"xhigh",
		"ul",
	)

	require.Equal(t, []string{"user_id = $1", "ul.reasoning_effort = $2"}, conditions)
	require.Equal(t, []any{int64(7), "xhigh"}, args)
}

func TestAppendUsageLogReasoningEffortWhereConditionIgnoresEmpty(t *testing.T) {
	conditions, args := appendUsageLogReasoningEffortWhereCondition(nil, nil, "  ")
	require.Empty(t, conditions)
	require.Empty(t, args)
}
