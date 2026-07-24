package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpsAlertRulePayloadMinimumRequestCount(t *testing.T) {
	decode := func(raw string) map[string]json.RawMessage {
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		return payload
	}

	_, err := validateOpsAlertRulePayload(decode(`{
		"name":"upstream errors",
		"metric_type":"upstream_error_rate",
		"operator":">",
		"threshold":5,
		"filters":{"minimum_request_count":100}
	}`))
	require.NoError(t, err)

	_, err = validateOpsAlertRulePayload(decode(`{
		"name":"upstream errors",
		"metric_type":"upstream_error_rate",
		"operator":">",
		"threshold":5,
		"filters":{"minimum_request_count":1.5}
	}`))
	require.ErrorContains(t, err, "minimum_request_count must be an integer")

	_, err = validateOpsAlertRulePayload(decode(`{
		"name":"queue depth",
		"metric_type":"concurrency_queue_depth",
		"operator":">",
		"threshold":10,
		"filters":{"minimum_request_count":100}
	}`))
	require.ErrorContains(t, err, "only supported for request rate metrics")
}
