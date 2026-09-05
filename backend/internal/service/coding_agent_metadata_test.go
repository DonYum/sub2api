package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractCodingAgentMetadata_ClientMachineHeaderWins(t *testing.T) {
	c := codingAgentMetadataContext(t, map[string]string{
		"X-Client-Machine":        "macbook-yf",
		"X-Codex-Installation-Id": "codex-install-1",
		"Originator":              "codex_cli_rs",
		"User-Agent":              "codex-cli/1.2.3",
	})

	meta := ExtractCodingAgentMetadata(c, []byte(`{"metadata":{"user_id":"{\"device_id\":\"device-1\",\"account_uuid\":\"account-1\",\"session_id\":\"session-1\"}"}}`), "203.0.113.9")

	require.Equal(t, "macbook-yf", meta.ClientMachineID)
	require.Equal(t, "x_client_machine", meta.ClientMachineSource)
	require.Equal(t, "device-1", meta.ClientDeviceID)
	require.Equal(t, "account-1", meta.ClientAccountUUID)
	require.Equal(t, "session-1", meta.CodexSessionID)
	require.Equal(t, "codex-install-1", meta.CodexInstallationID)
	require.Equal(t, "codex_cli_rs", meta.ClientOriginator)
	require.Len(t, meta.TerminalHash, 64)
}

func TestExtractCodingAgentMetadata_CodexTurnMetadataAndClientMetadata(t *testing.T) {
	c := codingAgentMetadataContext(t, map[string]string{
		"X-Codex-Turn-Metadata": `{"installation_id":"turn-install","session_id":"turn-session","thread_id":"turn-thread","turn_id":"turn-id","window_id":"turn-window","ignored":{"prompt":"nope"}}`,
		"X-Codex-Window-Id":     "header-window",
		"User-Agent":            "codex-cli/1.2.3",
	})
	body := []byte(`{
		"client_metadata": {
			"x-codex-installation-id": "body-install",
			"x-codex-window-id": "body-window",
			"thread_id": "body-thread",
			"turn_id": "body-turn",
			"x-codex-turn-metadata": "{\"installation_id\":\"body-turn-install\",\"session_id\":\"body-turn-session\"}"
		},
		"input": "prompt content must not be read"
	}`)

	meta := ExtractCodingAgentMetadata(c, body, "2001:db8::1")

	require.Equal(t, "turn-install", meta.ClientMachineID)
	require.Equal(t, "codex_installation", meta.ClientMachineSource)
	require.Equal(t, "turn-install", meta.CodexInstallationID)
	require.Equal(t, "header-window", meta.CodexWindowID)
	require.Equal(t, "turn-session", meta.CodexSessionID)
	require.Equal(t, "turn-thread", meta.CodexThreadID)
	require.Equal(t, "turn-id", meta.CodexTurnID)
	require.Len(t, meta.TerminalHash, 64)
}

func TestExtractCodingAgentMetadata_MetadataLegacyUserID(t *testing.T) {
	c := codingAgentMetadataContext(t, map[string]string{
		"User-Agent": "claude-cli/2.1.77",
	})
	deviceID := strings.Repeat("a", 64)
	accountUUID := "123e4567-e89b-12d3-a456-426614174000"
	sessionID := "223e4567-e89b-12d3-a456-426614174000"
	body := []byte(`{"metadata":{"user_id":"user_` + deviceID + `_account_` + accountUUID + `_session_` + sessionID + `"}}`)

	meta := ExtractCodingAgentMetadata(c, body, "198.51.100.123")

	require.Equal(t, deviceID, meta.ClientMachineID)
	require.Equal(t, "metadata_device", meta.ClientMachineSource)
	require.Equal(t, deviceID, meta.ClientDeviceID)
	require.Equal(t, accountUUID, meta.ClientAccountUUID)
	require.Equal(t, sessionID, meta.CodexSessionID)
	require.Len(t, meta.TerminalHash, 64)
}

func TestExtractCodingAgentMetadata_InvalidValuesAreDropped(t *testing.T) {
	c := codingAgentMetadataContext(t, map[string]string{
		"X-Client-Machine":        "bad\nmachine",
		"X-Codex-Installation-Id": strings.Repeat("x", maxCodingAgentIDLength+1),
		"X-Codex-Turn-Metadata":   strings.Repeat("x", maxCodexTurnMetadataSize+1),
		"User-Agent":              "codex-cli/1.2.3",
	})

	meta := ExtractCodingAgentMetadata(c, []byte(`{"metadata":{"user_id":"not-parseable"}}`), "203.0.113.4")

	require.Empty(t, meta.ClientMachineID)
	require.Empty(t, meta.ClientMachineSource)
	require.Empty(t, meta.CodexInstallationID)
	require.Empty(t, meta.ClientDeviceID)
	require.Len(t, meta.TerminalHash, 64, "weak UA/IP hash remains available for grouping")
}

func codingAgentMetadataContext(t *testing.T, headers map[string]string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}
