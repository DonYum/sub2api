package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// Gemini 流式上游偶发"接了请求却迟迟不出首个内容"或"200 但整段没有内容"，同一请求重发
// 一般几秒就能回来。这里在给客户端写出任何字节之前预读上游，直到看到首个内容事件：
// 超时或空流都判为本次尝试失败，由调用方重发。
const (
	geminiFirstOutputMaxAttempts = 3
	// 约 5 万 token 的请求体大小。
	geminiFirstOutputLargeBodyBytes = 200 * 1024
)

// 变量形式便于测试缩短。
var (
	geminiFirstOutputTimeout = 30 * time.Second
	// 超大上下文且未命中缓存时，上游正常也要 60～130 秒才出首字。
	geminiFirstOutputLargeTimeout = 150 * time.Second
)

var (
	errGeminiFirstOutputTimeout = errors.New("gemini upstream produced no output before first-output timeout")
	errGeminiFirstOutputEmpty   = errors.New("gemini upstream stream ended without any output")
)

func geminiFirstOutputTimeoutFor(requestBodyBytes int) time.Duration {
	if requestBodyBytes >= geminiFirstOutputLargeBodyBytes {
		return geminiFirstOutputLargeTimeout
	}
	return geminiFirstOutputTimeout
}

// geminiFirstOutputGuard 为一次上游尝试提供独立 context：到点仍未确认首个内容就取消该次请求。
// timeout<=0 时不计时（非流式请求的响应头要等整段生成完，不能套首字超时）。
type geminiFirstOutputGuard struct {
	ctx      context.Context
	cancel   context.CancelFunc
	timer    *time.Timer
	mu       sync.Mutex
	fired    bool
	released bool
}

func newGeminiFirstOutputGuard(parent context.Context, timeout time.Duration) *geminiFirstOutputGuard {
	ctx, cancel := context.WithCancel(parent)
	g := &geminiFirstOutputGuard{ctx: ctx, cancel: cancel}
	if timeout <= 0 {
		g.timer = time.NewTimer(time.Hour)
		g.timer.Stop()
		return g
	}
	g.timer = time.AfterFunc(timeout, func() {
		g.mu.Lock()
		fire := !g.released
		g.fired = fire
		g.mu.Unlock()
		if fire {
			cancel()
		}
	})
	return g
}

// Release 停止计时；返回 false 表示计时器已先触发（本次尝试已被取消）。
func (g *geminiFirstOutputGuard) Release() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fired {
		return false
	}
	g.released = true
	g.timer.Stop()
	return true
}

func (g *geminiFirstOutputGuard) TimedOut() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fired
}

// Abort 结束本次尝试并释放 context。
func (g *geminiFirstOutputGuard) Abort() {
	g.timer.Stop()
	g.cancel()
}

// prefetchGeminiStreamFirstOutput 读取上游 SSE 直到出现首个内容事件，返回可从头重放的 body。
// 流结束仍无内容且没有非 STOP 的 finishReason 时返回 errGeminiFirstOutputEmpty；
// 带 MAX_TOKENS / SAFETY 等结束原因的空流属于模型侧结果，原样放行。
func prefetchGeminiStreamFirstOutput(body io.Reader, isOAuth bool) (io.Reader, error) {
	reader := bufio.NewReader(body)
	var buf bytes.Buffer
	finishReason := ""
	for {
		line, err := reader.ReadString('\n')
		buf.WriteString(line)
		if hasContent, fr := geminiSSELineContent(line, isOAuth); hasContent {
			return io.MultiReader(bytes.NewReader(buf.Bytes()), reader), nil
		} else if fr != "" {
			finishReason = fr
		}
		if errors.Is(err, io.EOF) {
			if finishReason == "" || strings.EqualFold(finishReason, "STOP") {
				return nil, errGeminiFirstOutputEmpty
			}
			return bytes.NewReader(buf.Bytes()), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func geminiSSELineContent(line string, isOAuth bool) (bool, string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return false, ""
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return false, ""
	}
	raw := []byte(payload)
	if isOAuth {
		if inner, err := unwrapGeminiResponse(raw); err == nil {
			raw = inner
		}
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, ""
	}
	for _, part := range extractGeminiParts(resp) {
		if text, ok := part["text"].(string); ok && text != "" {
			return true, ""
		}
		if _, ok := part["functionCall"]; ok {
			return true, ""
		}
		if _, ok := part["inlineData"]; ok {
			return true, ""
		}
	}
	return false, extractGeminiFinishReason(resp)
}

// geminiGuardedBody 在 body 关闭时一并释放该次尝试的 context。
type geminiGuardedBody struct {
	io.Reader
	closer io.Closer
	guard  *geminiFirstOutputGuard
}

func (b *geminiGuardedBody) Close() error {
	err := b.closer.Close()
	b.guard.Abort()
	return err
}

// noteGeminiFirstOutputFailure 记录一次首字失败；未达上限返回 retry=true，
// 达到上限返回交给 handler 换号/渲染错误的 failover 错误（此时尚未向客户端写任何字节）。
func (s *GeminiMessagesCompatService) noteGeminiFirstOutputFailure(c *gin.Context, account *Account, failures *int, cause error, timeout time.Duration) (bool, error) {
	*failures++
	msg := cause.Error()
	if errors.Is(cause, errGeminiFirstOutputTimeout) {
		msg = fmt.Sprintf("%s (%s)", msg, timeout)
	}
	kind := "retry"
	if *failures >= geminiFirstOutputMaxAttempts {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: http.StatusBadGateway,
		Kind:               kind,
		Message:            msg,
	})
	logger.LegacyPrintf("service.gemini_chat_completions", "Gemini account %d: %s, attempt %d/%d", account.ID, msg, *failures, geminiFirstOutputMaxAttempts)
	if *failures < geminiFirstOutputMaxAttempts {
		return true, nil
	}
	setOpsUpstreamError(c, http.StatusBadGateway, msg, "")
	return false, &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: geminiTransportFailoverBody,
	}
}
