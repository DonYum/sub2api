package middleware

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type rawMessageRequestBody struct {
	io.ReadCloser
	capture io.Writer
}

func (r *rawMessageRequestBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.capture.Write(p[:n])
	}
	return n, err
}

type rawMessageResponseWriter struct {
	gin.ResponseWriter
	capture io.Writer
}

func (w *rawMessageResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		_, _ = w.capture.Write(p[:n])
	}
	return n, err
}

func (w *rawMessageResponseWriter) WriteString(s string) (int, error) {
	n, err := w.ResponseWriter.WriteString(s)
	if n > 0 {
		_, _ = w.capture.Write([]byte(s[:n]))
	}
	return n, err
}

// RawMessageCapture records gateway bodies only after API key authentication
// and only for keys that explicitly opted in. It never captures request or
// authentication headers; response Content-Type is allowlisted metadata.
func RawMessageCapture(rawService *service.RawMessageService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rawService == nil || !rawService.Enabled() || isWebSocketUpgrade(c.Request) {
			c.Next()
			return
		}
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || !apiKey.RawMessageRecordingEnabled {
			c.Next()
			return
		}
		clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		requestID := strings.TrimSpace(clientRequestID)
		if requestID != "" {
			requestID = "client:" + requestID
		}
		capture, err := rawService.BeginCapture(service.RawMessageCaptureInput{
			RequestID: requestID,
			APIKeyID:  apiKey.ID,
			UserID:    apiKey.UserID,
			Method:    c.Request.Method,
			Endpoint:  c.Request.URL.Path,
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			logger.FromContext(c.Request.Context()).Error("raw message capture start failed", zap.Error(err), zap.Int64("api_key_id", apiKey.ID))
			c.Next()
			return
		}
		if c.Request.Body != nil && c.Request.Body != http.NoBody {
			c.Request.Body = &rawMessageRequestBody{ReadCloser: c.Request.Body, capture: capture.RequestWriter()}
		}
		c.Writer = &rawMessageResponseWriter{ResponseWriter: c.Writer, capture: capture.ResponseWriter()}
		defer func() {
			if recovered := recover(); recovered != nil {
				capture.Discard()
				panic(recovered)
			}
			requestLogger := logger.FromContext(c.Request.Context())
			queued := rawService.FinalizeAsync(capture, c.Request.Context(), c.Writer.Status(), c.Writer.Header().Get("Content-Type"), func(err error) {
				requestLogger.Error("raw message capture finalize failed", zap.Error(err), zap.Int64("api_key_id", apiKey.ID), zap.String("request_id", requestID))
			})
			if !queued {
				requestLogger.Error("raw message capture finalize queue full; capture discarded", zap.Int64("api_key_id", apiKey.ID), zap.String("request_id", requestID))
			}
		}()
		c.Next()
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	return r != nil && strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}
