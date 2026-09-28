package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Separate from both JWT user routes and model API-key authentication.
func RegisterKeyDirectoryRoutes(v1 *gin.RouterGroup, h *handler.Handlers, limiter *middleware.PanelRateLimiter) {
	directory := v1.Group("/key-directory")
	directory.Use(h.KeyDirectory.Audit, limiter.PublicIP(), h.KeyDirectory.Authenticate, limiter.Global(), limiter.Heavy())
	directory.GET("", h.KeyDirectory.List)
	directory.POST("/resolve", h.KeyDirectory.Resolve)
}
