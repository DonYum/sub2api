package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummarizeOpenAIJSONShapeDoesNotExposeSensitiveValues(t *testing.T) {
	sentinels := []string{
		"SECRET_PROMPT_SENTINEL",
		"sk-secret-api-key",
		"Bearer secret-authorization",
		"private-tool-name",
		"call-secret-id",
		"private_property_name",
	}
	body := []byte(`{
		"model":"SECRET_PROMPT_SENTINEL",
		"authorization":"Bearer secret-authorization",
		"api_key":"sk-secret-api-key",
		"messages":[{"role":"user","content":"SECRET_PROMPT_SENTINEL"}],
		"tools":[{"type":"function","function":{"name":"private-tool-name","parameters":{"type":"object","properties":{"private_property_name":{"type":"string"}}}}}],
		"tool_calls":[{"id":"call-secret-id","function":{"name":"private-tool-name","arguments":"{\"private_property_name\":\"SECRET_PROMPT_SENTINEL\"}"}}]
	}`)

	summary, err := summarizeOpenAIJSONShape(body)
	require.NoError(t, err)
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)

	for _, sentinel := range sentinels {
		require.NotContains(t, string(encoded), sentinel)
	}
	require.Contains(t, summary.Records, "$.messages[] object fields=2")
	require.Contains(t, summary.Records, "$.messages[].content string bytes=22")
	require.Contains(t, summary.Records, "$.<key> string bytes=17")
}

func TestSummarizeOpenAIJSONShapeIsStableAcrossObjectKeyOrder(t *testing.T) {
	first, err := summarizeOpenAIJSONShape([]byte(`{"model":"a","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	require.NoError(t, err)
	second, err := summarizeOpenAIJSONShape([]byte(`{"stream":false,"messages":[{"content":"world","role":"user"}],"model":"b"}`))
	require.NoError(t, err)

	require.Equal(t, first.Hash, second.Hash)
	require.Equal(t, first.Records, second.Records)
}

func TestSummarizeOpenAIJSONShapeIgnoresDynamicKeyNames(t *testing.T) {
	first, err := summarizeOpenAIJSONShape([]byte(`{"properties":{"name":{"type":"string"},"id":{"type":"number"}}}`))
	require.NoError(t, err)
	second, err := summarizeOpenAIJSONShape([]byte(`{"properties":{"secret_one":{"type":"number"},"secret_two":{"type":"string"}}}`))
	require.NoError(t, err)

	require.Equal(t, first.Hash, second.Hash)
	require.Equal(t, first.Records, second.Records)
	encoded, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), ".name")
	require.NotContains(t, string(encoded), ".id")
}

func TestSummarizeOpenAIJSONShapeDistinguishesStructuralDifferences(t *testing.T) {
	base, err := summarizeOpenAIJSONShape([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	differentLength, err := summarizeOpenAIJSONShape([]byte(`{"messages":[{"role":"user","content":"hello"},{"role":"user","content":"again"}]}`))
	require.NoError(t, err)
	differentType, err := summarizeOpenAIJSONShape([]byte(`{"messages":{"role":"user","content":"hello"}}`))
	require.NoError(t, err)

	require.NotEqual(t, base.Hash, differentLength.Hash)
	require.NotEqual(t, base.Hash, differentType.Hash)
}

func TestSummarizeOpenAIJSONShapeBoundsVisibleRecords(t *testing.T) {
	values := make([]string, 0, maxOpenAIShapeRecords+10)
	for i := 0; i < maxOpenAIShapeRecords+10; i++ {
		values = append(values, strings.Repeat("x", i+1))
	}
	body, err := json.Marshal(map[string]any{"input": values})
	require.NoError(t, err)

	summary, err := summarizeOpenAIJSONShape(body)
	require.NoError(t, err)
	require.True(t, summary.Truncated)
	require.Len(t, summary.Records, maxOpenAIShapeRecords)
}
