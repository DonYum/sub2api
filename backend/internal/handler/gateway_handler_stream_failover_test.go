package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// partialMessageStartSSE 模拟 handleStreamingResponse 已写入的首批 SSE 事件。
const partialMessageStartSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_01\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-5\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"

// TestStreamWrittenGuard_MessagesPath_AbortFailoverOnSSEContentWritten 验证：
// 当 Forward 在返回 UpstreamFailoverError 前已向客户端写入 SSE 内容时，
// 故障转移保护逻辑必须终止循环并发送 SSE 错误事件，而不是进行下一次 Forward。
// 具体验证：
//  1. c.Writer.Size() 检测条件正确触发（字节数已增加）
//  2. handleFailoverExhausted 以 streamStarted=true 调用后，响应体以 SSE 错误事件结尾
//  3. 响应体中只出现一个 message_start，不存在第二个（防止流拼接腐化）
func TestStreamWrittenGuard_MessagesPath_AbortFailoverOnSSEContentWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	// 步骤 1：记录 Forward 前的 writer size（模拟 writerSizeBeforeForward := c.Writer.Size()）
	sizeBeforeForward := c.Writer.Size()
	require.Equal(t, -1, sizeBeforeForward, "gin writer 初始 Size 应为 -1（未写入任何字节）")

	// 步骤 2：模拟 Forward 已向客户端写入部分 SSE 内容（message_start + content_block_start）
	_, err := c.Writer.Write([]byte(partialMessageStartSSE))
	require.NoError(t, err)

	// 步骤 3：验证守卫条件成立（c.Writer.Size() != sizeBeforeForward）
	require.NotEqual(t, sizeBeforeForward, c.Writer.Size(),
		"写入 SSE 内容后 writer size 必须增加，守卫条件应为 true")

	// 步骤 4：模拟 UpstreamFailoverError（上游在流中途返回 403）
	failoverErr := &service.UpstreamFailoverError{
		StatusCode:   http.StatusForbidden,
		ResponseBody: []byte(`{"error":{"type":"permission_error","message":"forbidden"}}`),
	}

	// 步骤 5：守卫触发 → 调用 handleFailoverExhausted，streamStarted=true
	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, failoverErr, service.PlatformAnthropic, true)

	body := w.Body.String()

	// 断言 A：响应体中包含最初写入的 message_start SSE 事件行
	require.Contains(t, body, "event: message_start", "响应体应包含已写入的 message_start SSE 事件")

	// 断言 B：响应体以 SSE 错误事件结尾（data: {"type":"error",...}\n\n）
	require.True(t, strings.HasSuffix(strings.TrimRight(body, "\n"), "}"),
		"响应体应以 JSON 对象结尾（SSE error event 的 data 字段）")
	require.Contains(t, body, `"type":"error"`, "响应体末尾必须包含 SSE 错误事件")

	// 断言 C：SSE event 行 "event: message_start" 只出现一次（防止双 message_start 拼接腐化）
	firstIdx := strings.Index(body, "event: message_start")
	lastIdx := strings.LastIndex(body, "event: message_start")
	assert.Equal(t, firstIdx, lastIdx,
		"响应体中 'event: message_start' 必须只出现一次，不得因 failover 拼接导致两次")
}

// TestStreamWrittenGuard_GeminiPath_AbortFailoverOnSSEContentWritten 与上述测试相同，
// 验证 Gemini 路径使用 service.PlatformGemini（而非 account.Platform）时行为一致。
func TestStreamWrittenGuard_GeminiPath_AbortFailoverOnSSEContentWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:streamGenerateContent", nil)

	sizeBeforeForward := c.Writer.Size()

	_, err := c.Writer.Write([]byte(partialMessageStartSSE))
	require.NoError(t, err)

	require.NotEqual(t, sizeBeforeForward, c.Writer.Size())

	failoverErr := &service.UpstreamFailoverError{
		StatusCode: http.StatusForbidden,
	}

	h := &GatewayHandler{}
	h.handleFailoverExhausted(c, failoverErr, service.PlatformGemini, true)

	body := w.Body.String()

	require.Contains(t, body, "event: message_start")
	require.Contains(t, body, `"type":"error"`)

	firstIdx := strings.Index(body, "event: message_start")
	lastIdx := strings.LastIndex(body, "event: message_start")
	assert.Equal(t, firstIdx, lastIdx, "Gemini 路径不得出现双 message_start")
}

// TestStreamWrittenGuard_NoByteWritten_GuardNotTriggered 验证反向场景：
// 当 Forward 返回 UpstreamFailoverError 时若未向客户端写入任何 SSE 内容，
// 守卫条件（c.Writer.Size() != sizeBeforeForward）为 false，不应中止 failover。
func TestStreamWrittenGuard_NoByteWritten_GuardNotTriggered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	// 模拟 writerSizeBeforeForward：初始为 -1
	sizeBeforeForward := c.Writer.Size()

	// Forward 未写入任何字节直接返回错误（例如 401 发生在连接建立前）
	// c.Writer.Size() 仍为 -1

	// 守卫条件：sizeBeforeForward == c.Writer.Size() → 不触发
	guardTriggered := c.Writer.Size() != sizeBeforeForward
	require.False(t, guardTriggered,
		"未写入任何字节时，守卫条件必须为 false，应允许正常 failover 继续")
}

func TestOpenAIForwardCanFailoverOnlyBypassesWrittenGuardForExplicitSafeSignal(t *testing.T) {
	require.False(t, openAIForwardCanFailover(-1, 3, &service.UpstreamFailoverError{}))
	require.True(t, openAIForwardCanFailover(-1, 3, &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}))
	require.True(t, openAIForwardCanFailover(-1, -1, &service.UpstreamFailoverError{}))
}

func TestOpenAICapacitySafeFailoverCanReachLowerPriorityAccount(t *testing.T) {
	failed := &service.Account{Priority: 1}
	ordinaryFloor := updateOpenAIFailoverPriorityFloorForError(nil, failed, &service.UpstreamFailoverError{})
	require.NotNil(t, ordinaryFloor)
	require.Equal(t, 1, *ordinaryFloor)
	require.True(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(ordinaryFloor, &service.Account{Priority: 4}))

	capacityFloor := updateOpenAIFailoverPriorityFloorForError(nil, failed, &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true})
	require.Nil(t, capacityFloor)
	require.False(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(capacityFloor, &service.Account{Priority: 4}))
}

func TestOpenAIFailoverPriorityFloorMixedFailureSequence(t *testing.T) {
	capacity := &service.UpstreamFailoverError{SafeToFailoverAfterWrite: true}
	ordinary := &service.UpstreamFailoverError{}

	t.Run("capacity then ordinary restores floor for later attempts", func(t *testing.T) {
		floor := updateOpenAIFailoverPriorityFloorForError(nil, &service.Account{Priority: 1}, capacity)
		require.Nil(t, floor)
		require.False(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(floor, &service.Account{Priority: 4}))

		floor = updateOpenAIFailoverPriorityFloorForError(floor, &service.Account{Priority: 4}, ordinary)
		require.NotNil(t, floor)
		require.Equal(t, 4, *floor)
		require.True(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(floor, &service.Account{Priority: 10}))
	})

	t.Run("ordinary then capacity preserves existing floor", func(t *testing.T) {
		floor := updateOpenAIFailoverPriorityFloorForError(nil, &service.Account{Priority: 1}, ordinary)
		require.NotNil(t, floor)
		require.Equal(t, 1, *floor)

		floor = updateOpenAIFailoverPriorityFloorForError(floor, &service.Account{Priority: 4}, capacity)
		require.NotNil(t, floor)
		require.Equal(t, 1, *floor)
		require.True(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(floor, &service.Account{Priority: 4}))
		require.True(t, shouldStopOpenAIHTTPFailoverAtLowerPriority(floor, &service.Account{Priority: 10}))
	})
}

func TestOpenAIFailoverLoopCapacityThenOrdinaryStopsBeforeLowerPriorityForward(t *testing.T) {
	candidates := []*service.Account{
		{ID: 26, Priority: 1},
		{ID: 22, Priority: 4},
		{ID: 15, Priority: 10},
	}
	failures := map[int64]*service.UpstreamFailoverError{
		26: {SafeToFailoverAfterWrite: true},
		22: {},
	}
	failed := make(map[int64]struct{})
	var floor *int
	capacityRetries := 0
	forwarded := make([]int64, 0, 2)
	var blockedAccountID int64

	for {
		var selected *service.Account
		for _, candidate := range candidates {
			if _, excluded := failed[candidate.ID]; !excluded {
				selected = candidate
				break
			}
		}
		require.NotNil(t, selected)
		if shouldStopOpenAIHTTPFailoverAtLowerPriority(floor, selected) {
			blockedAccountID = selected.ID
			break
		}

		forwarded = append(forwarded, selected.ID)
		failoverErr := failures[selected.ID]
		require.NotNil(t, failoverErr)
		if failoverErr.SafeToFailoverAfterWrite {
			require.Less(t, capacityRetries, openAICapacityPrecommitRetryLimit)
			capacityRetries++
		}
		failed[selected.ID] = struct{}{}
		floor = updateOpenAIFailoverPriorityFloorForError(floor, selected, failoverErr)
	}

	require.Equal(t, []int64{26, 22}, forwarded)
	require.Equal(t, int64(15), blockedAccountID)
	require.Equal(t, 1, capacityRetries, "capacity retry count is global across accounts")
}

func TestOpenAIQueuePingBeforeForwardDoesNotBlockNormal503Failover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := c.Writer.Write([]byte(": ping\n\n"))
	require.NoError(t, err)
	sizeBeforeForward := c.Writer.Size()

	require.True(t, openAIForwardCanFailover(sizeBeforeForward, c.Writer.Size(), &service.UpstreamFailoverError{
		StatusCode: http.StatusServiceUnavailable,
	}), "bytes written before Forward must not count as this attempt's output")
}

func TestOpenAICapacityPrecommitRetryUsesIndependentTotalBudget(t *testing.T) {
	require.True(t, openAICapacityPrecommitRetryWithinBudget(time.Now().Add(-59*time.Second)))
	require.False(t, openAICapacityPrecommitRetryWithinBudget(time.Now().Add(-61*time.Second)))
	require.False(t, openAICapacityPrecommitRetryWithinBudget(time.Time{}))
	require.True(t, shouldRetryOpenAICapacityPrecommit(time.Now(), 0))
	require.True(t, shouldRetryOpenAICapacityPrecommit(time.Now(), 1))
	require.True(t, shouldRetryOpenAICapacityPrecommit(time.Now(), 2))
	require.False(t, shouldRetryOpenAICapacityPrecommit(time.Now(), 3), "precommit retry permits at most three account switches")
}

func TestOpenAICapacityPrecommitRetryRequiresDistinctAccounts(t *testing.T) {
	startedAt := time.Now()
	retriedAccountIDs := make(map[int64]struct{})
	retries := 0

	require.True(t, claimOpenAICapacityPrecommitRetry(startedAt, retries, 8, retriedAccountIDs))
	retries++
	require.False(t, claimOpenAICapacityPrecommitRetry(startedAt, retries, 8, retriedAccountIDs), "the same account must not consume another retry")
	require.True(t, claimOpenAICapacityPrecommitRetry(startedAt, retries, 11, retriedAccountIDs))
	retries++
	require.True(t, claimOpenAICapacityPrecommitRetry(startedAt, retries, 13, retriedAccountIDs))
	retries++
	require.False(t, claimOpenAICapacityPrecommitRetry(startedAt, retries, 14, retriedAccountIDs), "the fourth account switch must exceed the retry limit")
	require.False(t, claimOpenAICapacityPrecommitRetry(startedAt.Add(-61*time.Second), 0, 15, make(map[int64]struct{})), "an expired total budget must block the first retry")
}

func TestOpenAICapacityExhaustionAfterKeepaliveWritesResponsesSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := c.Writer.Write([]byte(":\n\n"))
	require.NoError(t, err)

	h := &OpenAIGatewayHandler{}
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{
		StatusCode:               http.StatusBadGateway,
		ResponseBody:             []byte(`{"error":{"message":"overloaded"}}`),
		SafeToFailoverAfterWrite: true,
	}, c.Writer.Written())

	body := w.Body.String()
	require.Contains(t, body, ":\n\n")
	require.Contains(t, body, "event: response.failed")
	require.NotContains(t, body, `{"error":{"type":"upstream_error"`)
}
