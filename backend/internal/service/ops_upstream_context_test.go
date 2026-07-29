package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeUpstreamErrorIdentifierTruncatesToDatabaseLimit(t *testing.T) {
	input := strings.Repeat("provider_error_", 8)
	got := sanitizeUpstreamErrorIdentifier(input)

	require.Len(t, got, 64)
	require.Equal(t, input[:64], got)
}

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}

func TestResolveOpsProviderErrorFields(t *testing.T) {
	tests := []struct {
		name        string
		event       *OpsUpstreamErrorEvent
		wantCode    string
		wantType    string
		wantFailure bool
	}{
		{
			name:     "explicit fields",
			event:    &OpsUpstreamErrorEvent{ProviderErrorCode: "server_is_overloaded", ProviderErrorType: "api_error"},
			wantCode: "server_is_overloaded", wantType: "api_error",
		},
		{
			name:     "object detail response error",
			event:    &OpsUpstreamErrorEvent{Detail: `{"response":{"error":{"code":"server_is_overloaded","type":"api_error"}}}`},
			wantCode: "server_is_overloaded", wantType: "api_error",
		},
		{
			name:     "stringified detail fallback error",
			event:    &OpsUpstreamErrorEvent{Detail: `"{\"error\":{\"code\":\"slow_down\",\"type\":\"rate_limit_error\"}}"`},
			wantCode: "slow_down", wantType: "rate_limit_error",
		},
		{
			name:  "valid json without provider code",
			event: &OpsUpstreamErrorEvent{Detail: `{"error":{"message":"unknown"}}`},
		},
		{
			name:        "malformed detail",
			event:       &OpsUpstreamErrorEvent{Detail: `{"response":`},
			wantFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, errorType, parseFailed := ResolveOpsProviderErrorFields([]*OpsUpstreamErrorEvent{tt.event})
			require.Equal(t, tt.wantCode, code)
			require.Equal(t, tt.wantType, errorType)
			require.Equal(t, tt.wantFailure, parseFailed)
		})
	}
}
