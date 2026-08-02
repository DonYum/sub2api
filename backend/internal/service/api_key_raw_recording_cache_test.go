package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotPreservesRawMessageRecordingGate(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	key := &APIKey{
		ID: 1, UserID: 2, Key: "test", Status: StatusActive,
		RawMessageRecordingEnabled: true,
		User:                       &User{ID: 2, Status: StatusActive, Role: RoleUser},
	}
	snapshot := svc.snapshotFromAPIKey(context.Background(), key)
	require.Equal(t, apiKeyAuthSnapshotVersion, snapshot.Version)
	require.True(t, snapshot.RawMessageRecordingEnabled)
	require.True(t, svc.snapshotToAPIKey(key.Key, snapshot).RawMessageRecordingEnabled)
}
