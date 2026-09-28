package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestKeyDirectoryRoutesHaveSeparateAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{KeyDirectory: handler.NewKeyDirectoryHandler(nil, nil), Admin: &handler.AdminHandlers{}}
	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(401) })
	audit := middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })
	RegisterAdminRoutes(r.Group("/api/v1"), h, adminAuth, audit, stepUp, nil, nil)
	RegisterKeyDirectoryRoutes(r.Group("/api/v1"), h, nil)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/admin/users/7/key-directory-credential"},
		{"DELETE", "/api/v1/admin/users/7/key-directory-credential"},
		{"GET", "/api/v1/key-directory"},
		{"POST", "/api/v1/key-directory/resolve"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, 401, w.Code)
		})
	}
}
