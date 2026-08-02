package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rawMessageTestRepo struct {
	mu      sync.Mutex
	created *service.RawMessageRecord
}

func (r *rawMessageTestRepo) Create(_ context.Context, record *service.RawMessageRecord) error {
	copy := *record
	copy.ID = 1
	r.mu.Lock()
	defer r.mu.Unlock()
	r.created = &copy
	record.ID = 1
	return nil
}

func (r *rawMessageTestRepo) getCreated() *service.RawMessageRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.created
}
func (r *rawMessageTestRepo) GetByRequestID(context.Context, string, int64) (*service.RawMessageRecord, error) {
	return nil, service.ErrRawMessageNotFound
}
func (r *rawMessageTestRepo) ExistingRequestIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (r *rawMessageTestRepo) PreviewCleanup(context.Context, service.RawMessageCleanupFilter) (*service.RawMessageCleanupPreview, error) {
	return &service.RawMessageCleanupPreview{}, nil
}
func (r *rawMessageTestRepo) ListCleanup(context.Context, service.RawMessageCleanupFilter) ([]service.RawMessageRecord, error) {
	return nil, nil
}
func (r *rawMessageTestRepo) DeleteByIDs(context.Context, []int64) error { return nil }

func TestRawMessageCapture_OptInCapturesBodiesWithoutHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &rawMessageTestRepo{}
	root := t.TempDir()
	svc := service.NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: true, DataDir: root, MaxRequestBytes: 1024, MaxResponseBytes: 1024,
	}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 9, RawMessageRecordingEnabled: true})
		ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "capture-test")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(RawMessageCapture(svc))
	router.POST("/v1/messages", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, `{"prompt":"secret"}`, string(body))
		c.JSON(http.StatusCreated, gin.H{"answer": "private"})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"secret"}`))
	req.Header.Set("Authorization", "Bearer must-not-be-recorded")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	require.Equal(t, http.StatusCreated, res.Code)
	require.Eventually(t, func() bool { return repo.getCreated() != nil }, time.Second, 10*time.Millisecond)
	created := repo.getCreated()
	require.Equal(t, "client:capture-test", created.RequestID)
	dir, err := svc.RecordDirectory(created)
	require.NoError(t, err)
	require.Equal(t, `{"prompt":"secret"}`, readGzipTestFile(t, filepath.Join(dir, "request.body.gz")))
	require.JSONEq(t, `{"answer":"private"}`, readGzipTestFile(t, filepath.Join(dir, "response.body.gz")))
	metadata, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	require.NoError(t, err)
	require.NotContains(t, string(metadata), "must-not-be-recorded")
	require.NotContains(t, string(metadata), "Authorization")
}

func TestRawMessageCapture_EnforcesIndependentBodyCaps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &rawMessageTestRepo{}
	svc := service.NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: true, DataDir: t.TempDir(), MaxRequestBytes: 4, MaxResponseBytes: 5,
	}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 2, RawMessageRecordingEnabled: true})
		ctx := context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "cap-test")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(RawMessageCapture(svc))
	router.POST("/test", func(c *gin.Context) { _, _ = io.ReadAll(c.Request.Body); _, _ = c.Writer.Write([]byte("abcdefgh")) })

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString("123456")))
	require.Equal(t, "abcdefgh", res.Body.String(), "capture must not truncate the client response")
	require.Eventually(t, func() bool { return repo.getCreated() != nil }, time.Second, 10*time.Millisecond)
	created := repo.getCreated()
	require.True(t, created.RequestTruncated)
	require.True(t, created.ResponseTruncated)
	dir, err := svc.RecordDirectory(created)
	require.NoError(t, err)
	require.Equal(t, "1234", readGzipTestFile(t, filepath.Join(dir, "request.body.gz")))
	require.Equal(t, "abcde", readGzipTestFile(t, filepath.Join(dir, "response.body.gz")))
}

func TestRawMessageCapture_DefaultOffDoesNotCreateFiles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &rawMessageTestRepo{}
	root := t.TempDir()
	svc := service.NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{Enabled: true, DataDir: root}})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 2}); c.Next() })
	router.Use(RawMessageCapture(svc))
	router.POST("/test", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("secret")))
	require.Nil(t, repo.getCreated())
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestRawMessageCapture_SkipsWebSocketUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &rawMessageTestRepo{}
	root := t.TempDir()
	svc := service.NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{Enabled: true, DataDir: root}})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 2, RawMessageRecordingEnabled: true})
		c.Next()
	})
	router.Use(RawMessageCapture(svc))
	router.GET("/responses", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })
	req := httptest.NewRequest(http.MethodGet, "/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	router.ServeHTTP(httptest.NewRecorder(), req)
	require.Nil(t, repo.getCreated())
}

func TestRawMessageCapture_DiscardsCaptureOnHandlerPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &rawMessageTestRepo{}
	root := t.TempDir()
	svc := service.NewRawMessageService(repo, &config.Config{RawMessageStorage: config.RawMessageStorageConfig{
		Enabled: true, DataDir: root,
	}})
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 2, RawMessageRecordingEnabled: true})
		c.Next()
	})
	router.Use(RawMessageCapture(svc))
	router.POST("/test", func(*gin.Context) { panic("handler failed") })

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/test", strings.NewReader("secret")))
	require.Equal(t, http.StatusInternalServerError, res.Code)
	require.Nil(t, repo.getCreated())
	entries, err := os.ReadDir(filepath.Join(root, ".tmp"))
	require.NoError(t, err)
	require.Empty(t, entries)
}

func readGzipTestFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	require.NoError(t, err)
	defer zr.Close()
	body, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(body)
}
