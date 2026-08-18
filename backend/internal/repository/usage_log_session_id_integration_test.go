//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestUsageLog_SessionIDPersistence proves session_id round-trips from insert to
// read and is omitted (NULL) when absent.
func TestUsageLog_SessionIDPersistence(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newUsageLogRepositoryWithSQL(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{Email: "session-id-" + uuid.NewString() + "@example.com"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-session-" + uuid.NewString(), Name: "k"})
	account := mustCreateAccount(t, client, &service.Account{Name: "acc-session-" + uuid.NewString()})

	sessionID := "sess-" + uuid.NewString()
	clientMachineID := "machine-" + uuid.NewString()
	clientMachineSource := "x_client_machine"
	clientDeviceID := "device-" + uuid.NewString()
	clientAccountUUID := uuid.NewString()
	clientOriginator := "codex_cli_rs"
	codexInstallationID := "install-" + uuid.NewString()
	codexWindowID := "window-" + uuid.NewString()
	codexSessionID := "codex-session-" + uuid.NewString()
	codexThreadID := "thread-" + uuid.NewString()
	codexTurnID := "turn-" + uuid.NewString()
	terminalHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	withSession := &service.UsageLog{
		UserID:              user.ID,
		APIKeyID:            apiKey.ID,
		AccountID:           account.ID,
		RequestID:           uuid.NewString(),
		Model:               "claude-3",
		InputTokens:         10,
		OutputTokens:        5,
		TotalCost:           1.0,
		ActualCost:          1.0,
		SessionID:           &sessionID,
		ClientMachineID:     &clientMachineID,
		ClientMachineSource: &clientMachineSource,
		ClientDeviceID:      &clientDeviceID,
		ClientAccountUUID:   &clientAccountUUID,
		ClientOriginator:    &clientOriginator,
		CodexInstallationID: &codexInstallationID,
		CodexWindowID:       &codexWindowID,
		CodexSessionID:      &codexSessionID,
		CodexThreadID:       &codexThreadID,
		CodexTurnID:         &codexTurnID,
		TerminalHash:        &terminalHash,
		CreatedAt:           time.Now().UTC(),
	}
	_, err := repo.Create(ctx, withSession)
	require.NoError(t, err)
	require.NotZero(t, withSession.ID)

	withoutSession := &service.UsageLog{
		UserID:       user.ID,
		APIKeyID:     apiKey.ID,
		AccountID:    account.ID,
		RequestID:    uuid.NewString(),
		Model:        "claude-3",
		InputTokens:  7,
		OutputTokens: 3,
		TotalCost:    0.5,
		ActualCost:   0.5,
		CreatedAt:    time.Now().UTC(),
	}
	_, err = repo.Create(ctx, withoutSession)
	require.NoError(t, err)

	// Round-trip: session id survives insert → read.
	got, err := repo.GetByID(ctx, withSession.ID)
	require.NoError(t, err)
	require.NotNil(t, got.SessionID)
	require.Equal(t, sessionID, *got.SessionID)
	require.NotNil(t, got.ClientMachineID)
	require.Equal(t, clientMachineID, *got.ClientMachineID)
	require.NotNil(t, got.ClientMachineSource)
	require.Equal(t, clientMachineSource, *got.ClientMachineSource)
	require.NotNil(t, got.ClientDeviceID)
	require.Equal(t, clientDeviceID, *got.ClientDeviceID)
	require.NotNil(t, got.ClientAccountUUID)
	require.Equal(t, clientAccountUUID, *got.ClientAccountUUID)
	require.NotNil(t, got.ClientOriginator)
	require.Equal(t, clientOriginator, *got.ClientOriginator)
	require.NotNil(t, got.CodexInstallationID)
	require.Equal(t, codexInstallationID, *got.CodexInstallationID)
	require.NotNil(t, got.CodexWindowID)
	require.Equal(t, codexWindowID, *got.CodexWindowID)
	require.NotNil(t, got.CodexSessionID)
	require.Equal(t, codexSessionID, *got.CodexSessionID)
	require.NotNil(t, got.CodexThreadID)
	require.Equal(t, codexThreadID, *got.CodexThreadID)
	require.NotNil(t, got.CodexTurnID)
	require.Equal(t, codexTurnID, *got.CodexTurnID)
	require.NotNil(t, got.TerminalHash)
	require.Equal(t, terminalHash, *got.TerminalHash)

	// Omission: absent session id reads back as nil (NULL), not empty string.
	gotNone, err := repo.GetByID(ctx, withoutSession.ID)
	require.NoError(t, err)
	require.Nil(t, gotNone.SessionID)
	require.Nil(t, gotNone.ClientMachineID)
	require.Nil(t, gotNone.ClientMachineSource)
	require.Nil(t, gotNone.ClientDeviceID)
	require.Nil(t, gotNone.ClientAccountUUID)
	require.Nil(t, gotNone.ClientOriginator)
	require.Nil(t, gotNone.CodexInstallationID)
	require.Nil(t, gotNone.CodexWindowID)
	require.Nil(t, gotNone.CodexSessionID)
	require.Nil(t, gotNone.CodexThreadID)
	require.Nil(t, gotNone.CodexTurnID)
	require.Nil(t, gotNone.TerminalHash)
}
