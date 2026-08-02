package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

var ErrRawMessageNotFound = infraerrors.NotFound("RAW_MESSAGE_NOT_FOUND", "raw message record not found")

type RawMessageRecord struct {
	ID                  int64     `json:"id"`
	RequestID           string    `json:"request_id"`
	APIKeyID            int64     `json:"api_key_id"`
	UserID              int64     `json:"user_id"`
	Method              string    `json:"method"`
	Endpoint            string    `json:"endpoint"`
	StatusCode          int       `json:"status_code"`
	ContentType         string    `json:"content_type,omitempty"`
	RelativePath        string    `json:"-"`
	RequestBytes        int64     `json:"request_bytes"`
	ResponseBytes       int64     `json:"response_bytes"`
	RequestStoredBytes  int64     `json:"request_stored_bytes"`
	ResponseStoredBytes int64     `json:"response_stored_bytes"`
	RequestTruncated    bool      `json:"request_truncated"`
	ResponseTruncated   bool      `json:"response_truncated"`
	CreatedAt           time.Time `json:"created_at"`
}

type RawMessageCleanupFilter struct {
	APIKeyID  int64
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
}

type RawMessageCleanupPreview struct {
	Records     int64 `json:"records"`
	StoredBytes int64 `json:"stored_bytes"`
}

type RawMessageCleanupResult struct {
	DeletedRecords   int64 `json:"deleted_records"`
	DeletedBytes     int64 `json:"deleted_bytes"`
	FailedRecords    int64 `json:"failed_records"`
	RemainingRecords int64 `json:"remaining_records"`
}

type RawMessageRepository interface {
	Create(ctx context.Context, record *RawMessageRecord) error
	GetByRequestID(ctx context.Context, requestID string, apiKeyID int64) (*RawMessageRecord, error)
	ExistingRequestIDs(ctx context.Context, requestIDs []string) (map[string]bool, error)
	PreviewCleanup(ctx context.Context, filter RawMessageCleanupFilter) (*RawMessageCleanupPreview, error)
	ListCleanup(ctx context.Context, filter RawMessageCleanupFilter) ([]RawMessageRecord, error)
	DeleteByIDs(ctx context.Context, ids []int64) error
}

type RawMessageService struct {
	repo          RawMessageRepository
	cfg           config.RawMessageStorageConfig
	root          string
	finalizeQueue chan rawMessageFinalizeJob
	cleanupMu     sync.Mutex
	capacityMu    sync.Mutex
	reservedBytes uint64
}

const (
	rawMessageFinalizeWorkers   = 2
	rawMessageFinalizeQueueSize = 128
)

type rawMessageFinalizeJob struct {
	capture     *RawMessageCapture
	ctx         context.Context
	statusCode  int
	contentType string
	onError     func(error)
}

func NewRawMessageService(repo RawMessageRepository, cfg *config.Config) *RawMessageService {
	storageCfg := config.RawMessageStorageConfig{}
	if cfg != nil {
		storageCfg = cfg.RawMessageStorage
	}
	root := strings.TrimSpace(storageCfg.DataDir)
	if root == "" {
		root = "./data/raw-messages"
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	if storageCfg.MaxRequestBytes <= 0 {
		storageCfg.MaxRequestBytes = 16 << 20
	}
	if storageCfg.MaxResponseBytes <= 0 {
		storageCfg.MaxResponseBytes = 16 << 20
	}
	if storageCfg.MinFreeBytes <= 0 {
		storageCfg.MinFreeBytes = 5 << 30
	}
	service := &RawMessageService{repo: repo, cfg: storageCfg, root: filepath.Clean(root)}
	if service.Enabled() {
		service.finalizeQueue = make(chan rawMessageFinalizeJob, rawMessageFinalizeQueueSize)
		for range rawMessageFinalizeWorkers {
			go service.runFinalizeWorker()
		}
	}
	return service
}

func (s *RawMessageService) runFinalizeWorker() {
	for job := range s.finalizeQueue {
		if err := job.capture.Finalize(job.ctx, job.statusCode, job.contentType); err != nil && job.onError != nil {
			job.onError(err)
		}
	}
}

// FinalizeAsync commits a completed capture outside the gateway response path,
// so gzip/fsync/database latency cannot delay the final SSE chunk or EOF. A
// bounded queue prevents capture persistence from creating unbounded goroutines.
// When the queue is full, the capture is discarded and false is returned.
func (s *RawMessageService) FinalizeAsync(capture *RawMessageCapture, ctx context.Context, statusCode int, contentType string, onError func(error)) bool {
	if s == nil || capture == nil || s.finalizeQueue == nil {
		if capture != nil {
			capture.Discard()
		}
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	job := rawMessageFinalizeJob{
		capture: capture, ctx: context.WithoutCancel(ctx), statusCode: statusCode,
		contentType: contentType, onError: onError,
	}
	select {
	case s.finalizeQueue <- job:
		return true
	default:
		capture.Discard()
		return false
	}
}

func (s *RawMessageService) Enabled() bool {
	return s != nil && s.repo != nil && s.cfg.Enabled
}

func (s *RawMessageService) available() bool {
	return s != nil && s.repo != nil
}

type RawMessageCaptureInput struct {
	RequestID string
	APIKeyID  int64
	UserID    int64
	Method    string
	Endpoint  string
	CreatedAt time.Time
}

type RawMessageCapture struct {
	service      *RawMessageService
	input        RawMessageCaptureInput
	tempDir      string
	requestFile  *os.File
	responseFile *os.File
	request      *cappedCaptureWriter
	response     *cappedCaptureWriter
	reserved     uint64
	finalizeOnce sync.Once
	finalizeErr  error
}

type cappedCaptureWriter struct {
	mu     sync.Mutex
	w      io.Writer
	limit  int64
	total  int64
	stored int64
}

func (w *cappedCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	remaining := w.limit - w.stored
	if remaining <= 0 {
		return len(p), nil
	}
	chunk := p
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
	}
	n, err := w.w.Write(chunk)
	w.stored += int64(n)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *cappedCaptureWriter) stats() (total, stored int64, truncated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total, w.stored, w.total > w.stored
}

func (s *RawMessageService) BeginCapture(input RawMessageCaptureInput) (*RawMessageCapture, error) {
	if !s.Enabled() {
		return nil, errors.New("raw message storage is disabled")
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("create raw message root: %w", err)
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return nil, fmt.Errorf("protect raw message root: %w", err)
	}
	// Reserve enough room for both capped raw bodies and their temporary gzip
	// copies. This closes the race where many captures all pass the same free
	// space check before any of them has written its body.
	reservation := 2*(uint64(s.cfg.MaxRequestBytes)+uint64(s.cfg.MaxResponseBytes)) + 1<<20
	s.capacityMu.Lock()
	var stat unix.Statfs_t
	if err := unix.Statfs(s.root, &stat); err != nil {
		s.capacityMu.Unlock()
		return nil, fmt.Errorf("check raw message storage capacity: %w", err)
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	required := uint64(s.cfg.MinFreeBytes) + s.reservedBytes + reservation
	if available < required {
		s.capacityMu.Unlock()
		return nil, fmt.Errorf("raw message storage free space below configured reserve")
	}
	s.reservedBytes += reservation
	s.capacityMu.Unlock()
	releaseReservation := func() { s.releaseReservation(reservation) }
	tmpRoot := filepath.Join(s.root, ".tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		releaseReservation()
		return nil, fmt.Errorf("create raw message temp root: %w", err)
	}
	tempDir, err := os.MkdirTemp(tmpRoot, "capture-")
	if err != nil {
		releaseReservation()
		return nil, fmt.Errorf("create raw message temp dir: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
		releaseReservation()
	}
	requestFile, err := os.OpenFile(filepath.Join(tempDir, "request.body"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("create request capture: %w", err)
	}
	responseFile, err := os.OpenFile(filepath.Join(tempDir, "response.body"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		_ = requestFile.Close()
		cleanup()
		return nil, fmt.Errorf("create response capture: %w", err)
	}
	return &RawMessageCapture{
		service: s, input: input, tempDir: tempDir,
		requestFile: requestFile, responseFile: responseFile,
		request:  &cappedCaptureWriter{w: requestFile, limit: s.cfg.MaxRequestBytes},
		response: &cappedCaptureWriter{w: responseFile, limit: s.cfg.MaxResponseBytes},
		reserved: reservation,
	}, nil
}

func (s *RawMessageService) releaseReservation(bytes uint64) {
	if s == nil || bytes == 0 {
		return
	}
	s.capacityMu.Lock()
	if bytes >= s.reservedBytes {
		s.reservedBytes = 0
	} else {
		s.reservedBytes -= bytes
	}
	s.capacityMu.Unlock()
}

func (c *RawMessageCapture) RequestWriter() io.Writer  { return c.request }
func (c *RawMessageCapture) ResponseWriter() io.Writer { return c.response }

func (c *RawMessageCapture) Finalize(ctx context.Context, statusCode int, contentType string) error {
	if c == nil {
		return nil
	}
	c.finalizeOnce.Do(func() {
		defer c.service.releaseReservation(c.reserved)
		c.finalizeErr = c.finalize(ctx, statusCode, contentType)
	})
	return c.finalizeErr
}

// Discard closes and removes an uncommitted capture. It shares finalizeOnce
// with Finalize so a capture can never be both committed and discarded.
func (c *RawMessageCapture) Discard() {
	if c == nil {
		return
	}
	c.finalizeOnce.Do(func() {
		defer c.service.releaseReservation(c.reserved)
		_ = c.requestFile.Close()
		_ = c.responseFile.Close()
		_ = os.RemoveAll(c.tempDir)
	})
}

func (c *RawMessageCapture) finalize(ctx context.Context, statusCode int, contentType string) error {
	defer func() { _ = os.RemoveAll(c.tempDir) }()
	if err := errors.Join(c.requestFile.Sync(), c.responseFile.Sync(), c.requestFile.Close(), c.responseFile.Close()); err != nil {
		return fmt.Errorf("close raw message capture: %w", err)
	}
	reqTotal, _, reqTruncated := c.request.stats()
	respTotal, _, respTruncated := c.response.stats()
	if err := gzipRawMessageFile(filepath.Join(c.tempDir, "request.body"), filepath.Join(c.tempDir, "request.body.gz")); err != nil {
		return fmt.Errorf("compress request capture: %w", err)
	}
	if err := gzipRawMessageFile(filepath.Join(c.tempDir, "response.body"), filepath.Join(c.tempDir, "response.body.gz")); err != nil {
		return fmt.Errorf("compress response capture: %w", err)
	}
	reqInfo, err := os.Stat(filepath.Join(c.tempDir, "request.body.gz"))
	if err != nil {
		return fmt.Errorf("stat request capture: %w", err)
	}
	respInfo, err := os.Stat(filepath.Join(c.tempDir, "response.body.gz"))
	if err != nil {
		return fmt.Errorf("stat response capture: %w", err)
	}
	reqStored, respStored := reqInfo.Size(), respInfo.Size()
	relativePath := filepath.Join(
		c.input.CreatedAt.UTC().Format("2006"),
		c.input.CreatedAt.UTC().Format("01"),
		c.input.CreatedAt.UTC().Format("02"),
		uuid.NewString(),
	)
	record := &RawMessageRecord{
		RequestID: strings.TrimSpace(c.input.RequestID), APIKeyID: c.input.APIKeyID, UserID: c.input.UserID,
		Method: c.input.Method, Endpoint: c.input.Endpoint, StatusCode: statusCode,
		ContentType: contentType, RelativePath: filepath.ToSlash(relativePath),
		RequestBytes: reqTotal, ResponseBytes: respTotal,
		RequestStoredBytes: reqStored, ResponseStoredBytes: respStored,
		RequestTruncated: reqTruncated, ResponseTruncated: respTruncated,
		CreatedAt: c.input.CreatedAt.UTC(),
	}
	metadata, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal raw message metadata: %w", err)
	}
	if err := os.WriteFile(filepath.Join(c.tempDir, "metadata.json"), metadata, 0o600); err != nil {
		return fmt.Errorf("write raw message metadata: %w", err)
	}
	finalDir, err := c.service.resolveRelativePath(record.RelativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o700); err != nil {
		return fmt.Errorf("create raw message date directory: %w", err)
	}
	if err := os.Rename(c.tempDir, finalDir); err != nil {
		return fmt.Errorf("commit raw message files: %w", err)
	}
	c.tempDir = ""
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.service.repo.Create(persistCtx, record); err != nil {
		_ = os.RemoveAll(finalDir)
		return fmt.Errorf("create raw message metadata: %w", err)
	}
	return nil
}

// gzipRawMessageFile runs only in a finalizer worker, after the gateway handler
// has returned. This keeps gzip CPU and fsync latency out of SSE chunk writes.
func gzipRawMessageFile(sourcePath, destinationPath string) (retErr error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeDestination := true
	destinationClosed := false
	defer func() {
		if !destinationClosed {
			if closeErr := destination.Close(); retErr == nil && closeErr != nil {
				retErr = closeErr
			}
		}
		if removeDestination {
			_ = os.Remove(destinationPath)
		}
	}()
	zw := gzip.NewWriter(destination)
	if _, err := io.Copy(zw, source); err != nil {
		_ = zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	destinationClosed = true
	removeDestination = false
	if err := os.Remove(sourcePath); err != nil {
		return err
	}
	return nil
}

func (s *RawMessageService) resolveRelativePath(relativePath string) (string, error) {
	relativePath = filepath.Clean(filepath.FromSlash(relativePath))
	if relativePath == "." || filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || relativePath == ".." {
		return "", errors.New("invalid raw message path")
	}
	resolved := filepath.Join(s.root, relativePath)
	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("raw message path escapes storage root")
	}
	return resolved, nil
}

func (s *RawMessageService) GetByRequestID(ctx context.Context, requestID string, apiKeyID int64) (*RawMessageRecord, error) {
	if !s.available() {
		return nil, ErrRawMessageNotFound
	}
	return s.repo.GetByRequestID(ctx, requestID, apiKeyID)
}

func (s *RawMessageService) ExistingRequestIDs(ctx context.Context, requestIDs []string) (map[string]bool, error) {
	if !s.available() || len(requestIDs) == 0 {
		return map[string]bool{}, nil
	}
	return s.repo.ExistingRequestIDs(ctx, requestIDs)
}

func RawMessageUsageKey(requestID string, apiKeyID int64) string {
	return fmt.Sprintf("%d:%s", apiKeyID, requestID)
}

func (s *RawMessageService) RecordDirectory(record *RawMessageRecord) (string, error) {
	if record == nil {
		return "", ErrRawMessageNotFound
	}
	return s.resolveRelativePath(record.RelativePath)
}

func (s *RawMessageService) PreviewCleanup(ctx context.Context, filter RawMessageCleanupFilter) (*RawMessageCleanupPreview, error) {
	if !s.available() {
		return nil, ErrRawMessageNotFound
	}
	if filter.APIKeyID <= 0 && filter.StartTime == nil && filter.EndTime == nil {
		return nil, infraerrors.BadRequest("RAW_MESSAGE_CLEANUP_FILTER_REQUIRED", "api_key_id or time range is required")
	}
	return s.repo.PreviewCleanup(ctx, filter)
}

func (s *RawMessageService) Cleanup(ctx context.Context, filter RawMessageCleanupFilter) (*RawMessageCleanupResult, error) {
	if !s.available() {
		return nil, ErrRawMessageNotFound
	}
	if filter.APIKeyID <= 0 && filter.StartTime == nil && filter.EndTime == nil {
		return nil, infraerrors.BadRequest("RAW_MESSAGE_CLEANUP_FILTER_REQUIRED", "api_key_id or time range is required")
	}
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 1000
	}
	records, err := s.repo.ListCleanup(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := &RawMessageCleanupResult{}
	ids := make([]int64, 0, len(records))
	type stagedDelete struct{ source, trash string }
	staged := make([]stagedDelete, 0, len(records))
	trashRoot := filepath.Join(s.root, ".trash")
	if err := os.MkdirAll(trashRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create raw message trash: %w", err)
	}
	for i := range records {
		dir, pathErr := s.RecordDirectory(&records[i])
		trashPath := filepath.Join(trashRoot, uuid.NewString())
		if pathErr != nil {
			result.FailedRecords++
			continue
		}
		renameErr := os.Rename(dir, trashPath)
		if renameErr != nil && !os.IsNotExist(renameErr) {
			result.FailedRecords++
			continue
		}
		ids = append(ids, records[i].ID)
		if renameErr == nil {
			staged = append(staged, stagedDelete{source: dir, trash: trashPath})
		}
		result.DeletedBytes += records[i].RequestStoredBytes + records[i].ResponseStoredBytes
	}
	if len(ids) > 0 {
		if err := s.repo.DeleteByIDs(ctx, ids); err != nil {
			for _, item := range staged {
				_ = os.Rename(item.trash, item.source)
			}
			return nil, err
		}
		result.DeletedRecords = int64(len(ids))
		for _, item := range staged {
			if os.RemoveAll(item.trash) != nil {
				result.FailedRecords++
			}
		}
	}
	remaining, err := s.repo.PreviewCleanup(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("verify raw message cleanup: %w", err)
	}
	result.RemainingRecords = remaining.Records
	return result, nil
}
