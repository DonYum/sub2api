package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const directoryUserID = "key_directory_user_id"

type KeyDirectoryHandler struct {
	service *service.KeyDirectoryService
	audit   *service.AuditLogService
}

func NewKeyDirectoryHandler(s *service.KeyDirectoryService, audit *service.AuditLogService) *KeyDirectoryHandler {
	return &KeyDirectoryHandler{service: s, audit: audit}
}

// Audit only operation metadata, never request/response bodies or credentials.
// Installed before authentication so denied requests are audited as well.
func (h *KeyDirectoryHandler) Audit(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	start := time.Now()
	c.Next()
	if h.audit == nil {
		return
	}
	entry := &service.AuditLog{CreatedAt: time.Now().UTC(), Action: "key_directory.read", Method: c.Request.Method, Path: c.FullPath(), ClientIP: middleware.SecurityClientIP(c), StatusCode: c.Writer.Status(), LatencyMs: time.Since(start).Milliseconds(), AuthMethod: "key_directory"}
	if id := c.GetInt64(directoryUserID); id > 0 {
		entry.ActorUserID = &id
	}
	if id := c.GetInt64("key_directory_resolved_id"); id > 0 {
		entry.Extra = map[string]any{"api_key_id": id}
	}
	h.audit.Record(entry)
}

func (h *KeyDirectoryHandler) Authenticate(c *gin.Context) {
	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		response.ErrorFrom(c, service.ErrKeyDirectoryCredential)
		c.Abort()
		return
	}
	user, err := h.service.Authenticate(c.Request.Context(), strings.TrimSpace(parts[1]))
	if err != nil {
		response.ErrorFrom(c, err)
		c.Abort()
		return
	}
	c.Set(directoryUserID, user.ID)
	// Reuse user-scoped panel rate limiting without granting an admin role.
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: user.ID, Concurrency: user.Concurrency})
	c.Next()
}

func (h *KeyDirectoryHandler) List(c *gin.Context) {
	userID := c.GetInt64(directoryUserID)
	if userID <= 0 {
		response.ErrorFrom(c, service.ErrKeyDirectoryCredential)
		return
	}
	page, size := response.ParsePagination(c)
	if size > 100 {
		size = 100
	}
	entries, result, err := h.service.List(c.Request.Context(), userID, pagination.PaginationParams{Page: page, PageSize: size, SortBy: "id", SortOrder: "asc"})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, entries, result.Total, page, size)
}

// Decode only the small documented request, rejecting user_id/scope overrides.
func decodeDirectoryRequest(c *gin.Context, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		response.BadRequest(c, "invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "invalid request body")
		return false
	}
	return true
}

func (h *KeyDirectoryHandler) Resolve(c *gin.Context) {
	userID := c.GetInt64(directoryUserID)
	if userID <= 0 {
		response.ErrorFrom(c, service.ErrKeyDirectoryCredential)
		return
	}
	var req struct {
		Name string `json:"api_key_name"`
		ID   *int64 `json:"id"`
	}
	if !decodeDirectoryRequest(c, &req) {
		return
	}
	connection, err := h.service.Resolve(c.Request.Context(), userID, req.Name, req.ID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Set("key_directory_resolved_id", connection.ID)
	response.Success(c, connection)
}

func (h *KeyDirectoryHandler) RotateCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "invalid user id")
		return
	}
	var req struct {
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decodeDirectoryRequest(c, &req) {
		return
	}
	token, err := h.service.RotateCredential(c.Request.Context(), userID, req.ExpiresAt)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"user_id": userID, "credential": token, "expires_at": req.ExpiresAt})
}

func (h *KeyDirectoryHandler) RevokeCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "invalid user id")
		return
	}
	if err := h.service.RevokeCredential(c.Request.Context(), userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}
