//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStripKiroGPTUnsupportedAnthropicFields_ResponsesUsesMappedModel(t *testing.T) {
	t.Parallel()

	var responsesReq apicompat.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"kiro-gpt-5.6-luna",
		"input":"hello",
		"reasoning":{"effort":"high"}
	}`), &responsesReq))

	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(&responsesReq)
	require.NoError(t, err)
	require.NotNil(t, anthropicReq.OutputConfig)
	require.NotNil(t, anthropicReq.Thinking)

	anthropicReq.Model = "gpt-5.6-luna"
	stripKiroGPTUnsupportedAnthropicFields(anthropicReq, "gpt-5.6-luna")

	body, err := json.Marshal(anthropicReq)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(body, "model").String())
	require.False(t, gjson.GetBytes(body, "output_config").Exists())
	require.False(t, gjson.GetBytes(body, "thinking").Exists())
}

func TestStripKiroGPTUnsupportedAnthropicFields_ChatCompletionsConversion(t *testing.T) {
	t.Parallel()

	var chatReq apicompat.ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"kiro-gpt-5.6-luna",
		"messages":[{"role":"user","content":"hello"}],
		"reasoning_effort":"high"
	}`), &chatReq))

	responsesReq, err := apicompat.ChatCompletionsToResponses(&chatReq)
	require.NoError(t, err)
	anthropicReq, err := apicompat.ResponsesToAnthropicRequest(responsesReq)
	require.NoError(t, err)
	require.NotNil(t, anthropicReq.OutputConfig)
	require.NotNil(t, anthropicReq.Thinking)

	anthropicReq.Model = "gpt-5.6-luna"
	stripKiroGPTUnsupportedAnthropicFields(anthropicReq, "gpt-5.6-luna")

	body, err := json.Marshal(anthropicReq)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "output_config").Exists())
	require.False(t, gjson.GetBytes(body, "thinking").Exists())
}

func TestStripKiroGPTUnsupportedAnthropicFields_PreservesClaudeThinking(t *testing.T) {
	t.Parallel()

	anthropicReq := &apicompat.AnthropicRequest{
		Model:        "claude-sonnet-5",
		OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "high"},
		Thinking:     &apicompat.AnthropicThinking{Type: "enabled", BudgetTokens: 10_000},
	}

	stripKiroGPTUnsupportedAnthropicFields(anthropicReq, "claude-sonnet-5")

	require.NotNil(t, anthropicReq.OutputConfig)
	require.NotNil(t, anthropicReq.Thinking)
}
