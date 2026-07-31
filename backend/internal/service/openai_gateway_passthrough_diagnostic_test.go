package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractOpenAIUpstream4xxDiagnostic(t *testing.T) {
	tests := []struct {
		name string
		body string
		want openAIUpstream4xxDiagnostic
	}{
		{
			name: "standard string fields",
			body: `{"error":{"message":"private content","type":"invalid_request_error","code":"invalid_value","param":"input[0].content"}}`,
			want: openAIUpstream4xxDiagnostic{Type: "invalid_request_error", Code: "invalid_value", Param: "input[0].content"},
		},
		{
			name: "numeric code",
			body: `{"error":{"type":"invalid_request_error","code":400,"param":null}}`,
			want: openAIUpstream4xxDiagnostic{Type: "invalid_request_error", Code: "400"},
		},
		{
			name: "unsafe values omitted",
			body: `{"error":{"type":"invalid request error","code":"secret=abc","param":"messages[0].content\nsecret"}}`,
			want: openAIUpstream4xxDiagnostic{},
		},
		{
			name: "invalid json",
			body: `{`,
			want: openAIUpstream4xxDiagnostic{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, extractOpenAIUpstream4xxDiagnostic([]byte(tt.body)))
		})
	}
}

func TestLogOpenAIUpstream4xxDiagnostic_AllowlistsFieldsWithoutContent(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()

	body := []byte(`{
		"api_key":"sk-private",
		"prompt":"TOP_SECRET_PROMPT",
		"error":{
			"message":"TOP_SECRET_MESSAGE",
			"type":"invalid_request_error",
			"code":"invalid_value",
			"param":"input[0].content"
		}
	}`)
	logOpenAIUpstream4xxDiagnostic(context.Background(), &Account{ID: 12}, 400, body)

	require.True(t, sink.ContainsMessage("openai.upstream_4xx_diagnostic"))
	require.True(t, sink.ContainsFieldValue("upstream_status", "400"))
	require.True(t, sink.ContainsFieldValue("account_id", "12"))
	require.True(t, sink.ContainsFieldValue("upstream_error_type", "invalid_request_error"))
	require.True(t, sink.ContainsFieldValue("upstream_error_code", "invalid_value"))
	require.True(t, sink.ContainsFieldValue("upstream_error_param", "input[0].content"))
	require.False(t, sink.ContainsField("message"))
	require.False(t, sink.ContainsField("body"))
	require.False(t, sink.ContainsField("prompt"))
	require.False(t, sink.ContainsField("api_key"))

	sink.mu.Lock()
	encoded, err := json.Marshal(sink.events)
	sink.mu.Unlock()
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "TOP_SECRET")
	require.NotContains(t, string(encoded), "sk-private")
}

func TestLogOpenAIUpstream4xxDiagnostic_IgnoresNon4xx(t *testing.T) {
	sink, cleanup := captureStructuredLog(t)
	defer cleanup()

	logOpenAIUpstream4xxDiagnostic(context.Background(), &Account{ID: 12}, 503, []byte(`{"error":{"type":"server_error"}}`))
	require.False(t, sink.ContainsMessage("openai.upstream_4xx_diagnostic"))
}
