package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rawDownloadUsageRepo struct {
	service.UsageLogRepository
	usage *service.UsageLog
}

func (r *rawDownloadUsageRepo) GetByID(context.Context, int64) (*service.UsageLog, error) {
	return r.usage, nil
}

type rawDownloadMessageRepo struct {
	record *service.RawMessageRecord
}

func (r *rawDownloadMessageRepo) Create(_ context.Context, record *service.RawMessageRecord) error {
	clone := *record
	clone.ID = 1
	r.record = &clone
	record.ID = 1
	return nil
}
func (r *rawDownloadMessageRepo) GetByRequestID(_ context.Context, requestID string, apiKeyID int64) (*service.RawMessageRecord, error) {
	if r.record == nil || r.record.RequestID != requestID || r.record.APIKeyID != apiKeyID {
		return nil, service.ErrRawMessageNotFound
	}
	clone := *r.record
	return &clone, nil
}
func (r *rawDownloadMessageRepo) ExistingRequestIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (r *rawDownloadMessageRepo) PreviewCleanup(context.Context, service.RawMessageCleanupFilter) (*service.RawMessageCleanupPreview, error) {
	return &service.RawMessageCleanupPreview{}, nil
}
func (r *rawDownloadMessageRepo) ListCleanup(context.Context, service.RawMessageCleanupFilter) ([]service.RawMessageRecord, error) {
	return nil, nil
}
func (r *rawDownloadMessageRepo) DeleteByIDs(context.Context, []int64) error { return nil }

func TestUsageHandlerDownloadRawMessageZIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rawRepo := &rawDownloadMessageRepo{}
	rawService := service.NewRawMessageService(rawRepo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: true, DataDir: t.TempDir(), MaxRequestBytes: 1024, MaxResponseBytes: 1024,
	}})
	capture, err := rawService.BeginCapture(service.RawMessageCaptureInput{
		RequestID: "client:req-1", APIKeyID: 7, UserID: 9, Method: http.MethodPost,
		Endpoint: "/v1/messages", CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = capture.RequestWriter().Write([]byte(`{"prompt":"private"}`))
	require.NoError(t, err)
	_, err = capture.ResponseWriter().Write([]byte("event: message\ndata: {\"answer\":\"private\"}\n\n"))
	require.NoError(t, err)
	require.NoError(t, capture.Finalize(context.Background(), http.StatusOK, "text/event-stream"))

	usageService := service.NewUsageService(&rawDownloadUsageRepo{usage: &service.UsageLog{
		ID: 11, RequestID: "client:req-1", APIKeyID: 7,
	}}, nil, nil, nil)
	handler := NewUsageHandler(usageService, nil, nil, nil)
	handler.SetRawMessageService(rawService)
	router := gin.New()
	router.GET("/usage/:id/raw-message", handler.DownloadRawMessage)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/usage/11/raw-message", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	zr, err := zip.NewReader(bytes.NewReader(recorder.Body.Bytes()), int64(recorder.Body.Len()))
	require.NoError(t, err)
	entries := make(map[string]string, len(zr.File))
	for _, file := range zr.File {
		reader, openErr := file.Open()
		require.NoError(t, openErr)
		body, readErr := io.ReadAll(reader)
		require.NoError(t, readErr)
		require.NoError(t, reader.Close())
		entries[file.Name] = string(body)
	}
	require.JSONEq(t, `{"prompt":"private"}`, entries["request.body"])
	require.Equal(t, "event: message\ndata: {\"answer\":\"private\"}\n\n", entries["response.body"])
	require.Contains(t, entries, "metadata.json")
}
