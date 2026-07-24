package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsAlertRateSampleIsSufficient(t *testing.T) {
	rule := &OpsAlertRule{Filters: map[string]any{"minimum_request_count": float64(10)}}
	require.False(t, opsAlertRateSampleIsSufficient(rule, 9))
	require.True(t, opsAlertRateSampleIsSufficient(rule, 10))
	require.True(t, opsAlertRateSampleIsSufficient(&OpsAlertRule{Filters: map[string]any{"minimum_request_count": "invalid"}}, 1))
	require.False(t, opsAlertRateSampleIsSufficient(rule, 0))
}
