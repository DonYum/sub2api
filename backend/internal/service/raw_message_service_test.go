package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type rawMessageServiceTestRepo struct {
	records   []RawMessageRecord
	deleteErr error
	deleted   []int64
}

func (r *rawMessageServiceTestRepo) Create(context.Context, *RawMessageRecord) error { return nil }
func (r *rawMessageServiceTestRepo) GetByRequestID(context.Context, string, int64) (*RawMessageRecord, error) {
	return nil, ErrRawMessageNotFound
}
func (r *rawMessageServiceTestRepo) ExistingRequestIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (r *rawMessageServiceTestRepo) PreviewCleanup(context.Context, RawMessageCleanupFilter) (*RawMessageCleanupPreview, error) {
	var stored int64
	for i := range r.records {
		stored += r.records[i].RequestStoredBytes + r.records[i].ResponseStoredBytes
	}
	return &RawMessageCleanupPreview{Records: int64(len(r.records)), StoredBytes: stored}, nil
}
func (r *rawMessageServiceTestRepo) ListCleanup(context.Context, RawMessageCleanupFilter) ([]RawMessageRecord, error) {
	return slices.Clone(r.records), nil
}
func (r *rawMessageServiceTestRepo) DeleteByIDs(_ context.Context, ids []int64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, ids...)
	remaining := r.records[:0]
	for i := range r.records {
		if !slices.Contains(ids, r.records[i].ID) {
			remaining = append(remaining, r.records[i])
		}
	}
	r.records = remaining
	return nil
}

func newRawMessageServiceForCleanupTest(t *testing.T, repo RawMessageRepository) (*RawMessageService, string) {
	t.Helper()
	root := t.TempDir()
	svc := NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: true,
		DataDir: root,
	}})
	return svc, root
}

func TestRawMessageCleanupStagesFilesBeforeDeletingMetadata(t *testing.T) {
	repo := &rawMessageServiceTestRepo{records: []RawMessageRecord{{
		ID: 1, RelativePath: "2026/08/02/capture-1", RequestStoredBytes: 3, ResponseStoredBytes: 4,
	}}}
	svc, root := newRawMessageServiceForCleanupTest(t, repo)
	dir := filepath.Join(root, filepath.FromSlash(repo.records[0].RelativePath))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{}"), 0o600))

	result, err := svc.Cleanup(context.Background(), RawMessageCleanupFilter{APIKeyID: 7})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.DeletedRecords)
	require.Equal(t, int64(7), result.DeletedBytes)
	require.Zero(t, result.RemainingRecords)
	require.Equal(t, []int64{1}, repo.deleted)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))
}

func TestRawMessageCleanupRestoresFilesWhenMetadataDeleteFails(t *testing.T) {
	repo := &rawMessageServiceTestRepo{
		records:   []RawMessageRecord{{ID: 2, RelativePath: "2026/08/02/capture-2"}},
		deleteErr: errors.New("database unavailable"),
	}
	svc, root := newRawMessageServiceForCleanupTest(t, repo)
	dir := filepath.Join(root, filepath.FromSlash(repo.records[0].RelativePath))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{}"), 0o600))

	_, err := svc.Cleanup(context.Background(), RawMessageCleanupFilter{APIKeyID: 7})
	require.ErrorContains(t, err, "database unavailable")
	_, err = os.Stat(filepath.Join(dir, "metadata.json"))
	require.NoError(t, err)
}

func TestRawMessageCleanupRejectsUnboundedDelete(t *testing.T) {
	svc, _ := newRawMessageServiceForCleanupTest(t, &rawMessageServiceTestRepo{})
	_, err := svc.Cleanup(context.Background(), RawMessageCleanupFilter{})
	require.Error(t, err)
}

func TestRawMessageCleanupRemainsAvailableWhenCaptureKillSwitchIsOff(t *testing.T) {
	repo := &rawMessageServiceTestRepo{records: []RawMessageRecord{{ID: 3, RelativePath: "2026/08/02/capture-3"}}}
	root := t.TempDir()
	svc := NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: false,
		DataDir: root,
	}})
	dir := filepath.Join(root, filepath.FromSlash(repo.records[0].RelativePath))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.json"), []byte("{}"), 0o600))

	result, err := svc.Cleanup(context.Background(), RawMessageCleanupFilter{APIKeyID: 7})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.DeletedRecords)
	require.Zero(t, result.RemainingRecords)
}
