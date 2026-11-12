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
	// 小请求的补发间隔：首份 30 秒没出字就并发补发一份，再过 30 秒再补一份；
	// 已发出的尝试不取消，共用 geminiFirstOutputMaxAttempts×间隔 的总等待（90 秒）。
	geminiFirstOutputHedgeDelay = 30 * time.Second
	geminiFirstOutputTimeout    = 30 * time.Second
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

// geminiHedgeOutcome 是一份并发尝试的结果：won=拿到首个内容；decisive=非 200 响应，交回调用方
// 按原有错误处理；两者都不是即失败（超时 / 空流 / 传输错误）。
type geminiHedgeOutcome struct {
	resp     *http.Response
	idHeader string
	won      bool
	decisive bool
	failure  error
	guard    *geminiFirstOutputGuard
}

// raceGeminiStreamFirstOutput 对小请求做错峰并发：先发一份，hedgeDelay 内没出字就再发一份，
// 最多 maxAttempts 份；已发出的不取消，所有尝试共用同一个截止时间，首个出字的胜出，其余取消。
// 某份提前失败（空流）时立即补发下一份。只在主 goroutine 里写 gin 上下文（ops 记录）。
func (s *GeminiMessagesCompatService) raceGeminiStreamFirstOutput(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	buildReq func(context.Context) (*http.Request, string, error),
	proxyURL string,
) (*http.Response, string, error) {
	isOAuth := account.Type == AccountTypeOAuth
	budget := geminiFirstOutputHedgeDelay * geminiFirstOutputMaxAttempts
	deadline := time.Now().Add(budget)
	results := make(chan geminiHedgeOutcome, geminiFirstOutputMaxAttempts)
	var guards []*geminiFirstOutputGuard
	launched, finished, failures := 0, 0, 0

	launch := func() {
		launched++
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		guard := newGeminiFirstOutputGuard(ctx, remaining)
		guards = append(guards, guard)
		go func() {
			out := geminiHedgeOutcome{guard: guard}
			req, idHeader, err := buildReq(guard.ctx)
			if err != nil {
				out.failure = err
				results <- out
				return
			}
			out.idHeader = idHeader
			resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
			if err != nil {
				out.failure = err
				if guard.TimedOut() {
					out.failure = errGeminiFirstOutputTimeout
				}
				results <- out
				return
			}
			resp.Body = &geminiGuardedBody{Reader: resp.Body, closer: resp.Body, guard: guard}
			if resp.StatusCode != http.StatusOK {
				out.resp, out.decisive = resp, true
				results <- out
				return
			}
			replay, prefetchErr := prefetchGeminiStreamFirstOutput(resp.Body, isOAuth)
			if prefetchErr == nil && guard.Release() {
				resp.Body = &geminiGuardedBody{Reader: replay, closer: resp.Body, guard: guard}
				out.resp, out.won = resp, true
				results <- out
				return
			}
			_ = resp.Body.Close()
			out.failure = prefetchErr
			if guard.TimedOut() || prefetchErr == nil {
				out.failure = errGeminiFirstOutputTimeout
			}
			results <- out
		}()
	}

	// 胜出后取消其余尝试，并在后台回收它们的响应体。
	settle := func(winner *geminiFirstOutputGuard) {
		for _, g := range guards {
			if g != winner {
				g.Abort()
			}
		}
		pending := launched - finished
		if pending == 0 {
			return
		}
		go func() {
			for i := 0; i < pending; i++ {
				if out := <-results; out.resp != nil {
					_ = out.resp.Body.Close()
				}
			}
		}()
	}

	launch()
	hedge := time.NewTimer(geminiFirstOutputHedgeDelay)
	defer hedge.Stop()
	for {
		select {
		case <-ctx.Done():
			settle(nil)
			return nil, "", ctx.Err()
		case <-hedge.C:
			if launched < geminiFirstOutputMaxAttempts {
				logger.LegacyPrintf("service.gemini_chat_completions", "Gemini account %d: no output after %s, hedging attempt %d/%d", account.ID, geminiFirstOutputHedgeDelay*time.Duration(launched), launched+1, geminiFirstOutputMaxAttempts)
				launch()
				hedge.Reset(geminiFirstOutputHedgeDelay)
			}
		case out := <-results:
			finished++
			if out.won || out.decisive {
				settle(out.guard)
				return out.resp, out.idHeader, nil
			}
			if ctx.Err() != nil {
				settle(nil)
				return nil, "", ctx.Err()
			}
			if !errors.Is(out.failure, errGeminiFirstOutputTimeout) && !errors.Is(out.failure, errGeminiFirstOutputEmpty) {
				// 传输层错误：还有在途尝试就等它们，否则交回调用方按原有传输错误处理（换号 / 摘号）。
				if launched-finished > 0 {
					continue
				}
				settle(nil)
				return nil, "", out.failure
			}
			retry, failErr := s.noteGeminiFirstOutputFailure(c, account, &failures, out.failure, budget)
			if !retry {
				settle(nil)
				return nil, "", failErr
			}
			if launched < geminiFirstOutputMaxAttempts && launched-finished == 0 {
				launch()
				hedge.Reset(geminiFirstOutputHedgeDelay)
			}
		}
	}
}
