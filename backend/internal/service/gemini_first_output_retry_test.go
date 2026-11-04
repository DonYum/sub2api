//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// geminiSeqUpstreamStub 按调用次序返回不同的流式响应；nil 项表示"上游一直不出内容"，
// 其 body 阻塞到该次请求的 context 被取消。
type geminiSeqUpstreamStub struct {
	bodies []*string
	calls  int
}

type geminiBlockingBody struct{ ctx context.Context }

func (b geminiBlockingBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (geminiBlockingBody) Close() error               { return nil }

func (s *geminiSeqUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	i := s.calls
	s.calls++
	var body io.ReadCloser = geminiBlockingBody{ctx: req.Context()}
	if i < len(s.bodies) && s.bodies[i] != nil {
		body = io.NopCloser(strings.NewReader(*s.bodies[i]))
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
}

func (s *geminiSeqUpstreamStub) DoWithTLS(req *http.Request, p string, id int64, c int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, p, id, c)
}

func strp(s string) *string { return &s }

const (
	geminiOKStream    = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"pong\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":1}}\n\n"
	geminiEmptyStream = "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5}}\n\n"
)

func runGeminiChatStream(t *testing.T, stub *geminiSeqUpstreamStub) (*ForwardResult, error, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	old := geminiFirstOutputTimeout
	geminiFirstOutputTimeout = 50 * time.Millisecond
	t.Cleanup(func() { geminiFirstOutputTimeout = old })

	svc := &GeminiMessagesCompatService{httpUpstream: stub, cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gemini-3.8-flash","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	acct := &Account{ID: 29, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}}
	res, err := svc.ForwardAsChatCompletions(context.Background(), c, acct, body)
	return res, err, rec
}

func TestGeminiChatStream_RetriesAfterFirstOutputTimeout(t *testing.T) {
	stub := &geminiSeqUpstreamStub{bodies: []*string{nil, strp(geminiOKStream)}}
	res, err, rec := runGeminiChatStream(t, stub)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 2, stub.calls)
	require.Contains(t, rec.Body.String(), "pong")
	require.Contains(t, rec.Body.String(), "[DONE]")
}

func TestGeminiChatStream_RetriesEmptyStream(t *testing.T) {
	stub := &geminiSeqUpstreamStub{bodies: []*string{strp(geminiEmptyStream), strp(geminiOKStream)}}
	res, err, rec := runGeminiChatStream(t, stub)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 2, stub.calls)
	require.Equal(t, 1, strings.Count(rec.Body.String(), "\"role\":\"assistant\""), "失败的那次不应写给客户端")
	require.Contains(t, rec.Body.String(), "pong")
}

func TestGeminiChatStream_GivesUpAfterMaxAttemptsWithoutWriting(t *testing.T) {
	stub := &geminiSeqUpstreamStub{bodies: []*string{nil, strp(geminiEmptyStream), nil, strp(geminiOKStream)}}
	res, err, rec := runGeminiChatStream(t, stub)
	require.Nil(t, res)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr), "got %T: %v", err, err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, geminiFirstOutputMaxAttempts, stub.calls)
	require.Zero(t, rec.Body.Len(), "放弃时不应已向客户端写任何字节")
}

func TestGeminiChatStream_MaxTokensEmptyStreamPassesThrough(t *testing.T) {
	maxTok := "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"MAX_TOKENS\"}]}\n\n"
	stub := &geminiSeqUpstreamStub{bodies: []*string{strp(maxTok)}}
	_, err, rec := runGeminiChatStream(t, stub)
	require.NoError(t, err)
	require.Equal(t, 1, stub.calls, "模型侧结束原因不重试")
	require.Contains(t, rec.Body.String(), "\"finish_reason\":\"length\"")
}

func TestGeminiFirstOutputTimeoutFor(t *testing.T) {
	require.Equal(t, geminiFirstOutputTimeout, geminiFirstOutputTimeoutFor(1000))
	require.Equal(t, geminiFirstOutputLargeTimeout, geminiFirstOutputTimeoutFor(geminiFirstOutputLargeBodyBytes))
}
