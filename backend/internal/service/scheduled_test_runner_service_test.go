//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildScheduledTestOpsErrorLogIncludesAccountID(t *testing.T) {
	startedAt := time.Unix(1724130000, 0)
	plan := &ScheduledTestPlan{ID: 123, AccountID: 77, ModelID: "gpt-5.6"}
	result := &ScheduledTestResult{
		Status:       "failed",
		ErrorMessage: "API returned 503: server overloaded",
		StartedAt:    startedAt,
		FinishedAt:   startedAt.Add(time.Second),
	}

	entry := buildScheduledTestOpsErrorLog(plan, result)

	require.NotNil(t, entry)
	require.NotNil(t, entry.AccountID)
	require.EqualValues(t, 77, *entry.AccountID)
	require.Equal(t, "scheduled-test-123-1724130000000000000", entry.RequestID)
	require.Equal(t, "gpt-5.6", entry.Model)
	require.Equal(t, "gpt-5.6", entry.RequestedModel)
	require.Equal(t, "scheduled_test:123", entry.RequestPath)
	require.Equal(t, "scheduled_test", entry.ErrorPhase)
	require.Equal(t, "scheduled_test", entry.ErrorSource)
	require.Equal(t, "gateway", entry.ErrorOwner)
	require.Equal(t, "upstream_error", entry.ErrorType)
	require.Equal(t, "P1", entry.Severity)
	require.Equal(t, http.StatusInternalServerError, entry.StatusCode)
	require.Equal(t, "API returned 503: server overloaded", entry.ErrorMessage)
}
