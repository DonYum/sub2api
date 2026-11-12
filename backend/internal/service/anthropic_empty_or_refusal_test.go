package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newAnthropicEmptyFailoverService() *GatewayService {
	return &GatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
		},
		rateLimitService: &RateLimitService{},
	}
}

func anthropicSSEResponse(body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	header.Set("x-request-id", "rid-anthropic-sse")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func anthropicJSONResponse(body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("x-request-id", "rid-anthropic-json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestAnthropicPassthroughStream_RefusalDoesNotWriteAndFailovers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Set("anthropic_passthrough", true)

	// kiro.rs surfaces CONTENT_FILTERED as HTTP 200 + stop_reason=refusal.
	// The explanation lives in stop_details and must not count as assistant text.
	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"content_filtered","explanation":"temporary model restriction"}},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), anthropicSSEResponse(body), c, &Account{ID: 41, Name: "kiro", Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.True(t, IsAnthropicEmptyOrRefusalErrorBody(failoverErr.ResponseBody))
	require.Contains(t, string(failoverErr.ResponseBody), "stop_reason=refusal")
	require.Equal(t, "rid-anthropic-sse", failoverErr.ResponseHeaders.Get("x-request-id"))
	require.False(t, c.Writer.Written(), "refusal must not commit a 200 stream before failover")
	require.Empty(t, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "message_start")
	require.NotContains(t, rec.Body.String(), "temporary model restriction")
}

func TestAnthropicPassthroughStream_EmptyEndTurnDoesNotWriteAndFailovers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":8,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"  "}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), anthropicSSEResponse(body), c, &Account{ID: 42, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, IsAnthropicEmptyOrRefusalErrorBody(failoverErr.ResponseBody))
	require.Contains(t, string(failoverErr.ResponseBody), "stop_reason=end_turn")
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
}

func TestAnthropicPassthroughStream_RealTextFlushesBufferedEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_3","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":4,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), anthropicSSEResponse(body), c, &Account{ID: 43, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.usage)
	require.Equal(t, 4, result.usage.InputTokens)
	require.Equal(t, 2, result.usage.OutputTokens)
	require.Contains(t, rec.Body.String(), "message_start")
	require.Contains(t, rec.Body.String(), `"text":"Hello"`)
	require.Contains(t, rec.Body.String(), "message_stop")
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
}

func TestAnthropicPassthroughStream_ToolUseAndThinkingReleaseWithoutFailover(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "tool_use",
			payload: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
			want:    `"name":"Bash"`,
		},
		{
			name:    "thinking",
			payload: `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan the edit"}}`,
			want:    "plan the edit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := strings.Join([]string{
				`event: message_start`,
				`data: {"type":"message_start","message":{"usage":{"input_tokens":2}}}`,
				``,
				tc.payload,
				``,
				`event: message_delta`,
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				``,
				`event: message_stop`,
				`data: {"type":"message_stop"}`,
				``,
			}, "\n")

			result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
				context.Background(), anthropicSSEResponse(body), c, &Account{ID: 44, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
			)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Contains(t, rec.Body.String(), tc.want)
		})
	}
}

func TestAnthropicPassthroughStream_RefusalAfterWrittenTextDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	body := strings.Join([]string{
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answer"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","explanation":"too late"}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), anthropicSSEResponse(body), c, &Account{ID: 45, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "partial answer")
	require.Contains(t, rec.Body.String(), `"stop_reason":"refusal"`)
	require.True(t, c.Writer.Written())
}

func TestAnthropicPassthroughStream_ReadErrorBeforeWriteFailovers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	resp := anthropicSSEResponse("")
	resp.Body = &streamReadCloser{err: io.ErrUnexpectedEOF}

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), resp, c, &Account{ID: 46, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Contains(t, ExtractUpstreamErrorMessage(failoverErr.ResponseBody), "upstream stream disconnected")
	require.Contains(t, string(failoverErr.ResponseBody), `"upstream_disconnected"`)
	require.False(t, c.Writer.Written())
	require.Empty(t, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "stream_read_error")
}

func TestAnthropicPassthroughStream_ReadErrorAfterTextDoesNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	resp := anthropicSSEResponse("")
	resp.Body = &streamReadCloser{
		payload: []byte(strings.Join([]string{
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			``,
		}, "\n")),
		err: io.ErrUnexpectedEOF,
	}

	result, err := newAnthropicEmptyFailoverService().handleStreamingResponseAnthropicAPIKeyPassthrough(
		context.Background(), resp, c, &Account{ID: 47, Platform: PlatformAnthropic}, time.Now(), "claude-sonnet-4-5",
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "stream read error")
	require.NotNil(t, result)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Contains(t, rec.Body.String(), "hello")
}

func TestAnthropicNonStreaming_RefusalAndEmptyEndTurnFailover(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "refusal with stop_details",
			body: `{"id":"msg_r","type":"message","role":"assistant","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"content_filtered","explanation":"model restriction"},"usage":{"input_tokens":3,"output_tokens":1}}`,
		},
		{
			name: "refusal explanation text still failovers before write",
			body: `{"id":"msg_r2","type":"message","role":"assistant","content":[{"type":"text","text":"I must refuse this"}],"stop_reason":"refusal","usage":{"input_tokens":3,"output_tokens":4}}`,
		},
		{
			name: "empty end_turn",
			body: `{"id":"msg_e","type":"message","role":"assistant","content":[{"type":"text","text":""}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":0}}`,
		},
		{
			name: "empty stop_sequence",
			body: `{"id":"msg_s","type":"message","role":"assistant","content":[],"stop_reason":"stop_sequence","usage":{"input_tokens":1,"output_tokens":0}}`,
		},
	}
	for _, tc := range cases {
		t.Run("passthrough/"+tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			usage, err := newAnthropicEmptyFailoverService().handleNonStreamingResponseAnthropicAPIKeyPassthrough(
				context.Background(), anthropicJSONResponse(tc.body), c, &Account{ID: 48, Platform: PlatformAnthropic},
			)
			require.Nil(t, usage)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.True(t, IsAnthropicEmptyOrRefusalErrorBody(failoverErr.ResponseBody))
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
		t.Run("messages/"+tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			usage, err := newAnthropicEmptyFailoverService().handleNonStreamingResponse(
				context.Background(), anthropicJSONResponse(tc.body), c, &Account{ID: 49, Platform: PlatformAnthropic}, "claude-sonnet-4-5", "claude-sonnet-4-5",
			)
			require.Nil(t, usage)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.True(t, IsAnthropicEmptyOrRefusalErrorBody(failoverErr.ResponseBody))
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
		})
	}
}

func TestAnthropicNonStreaming_RealOutputUnchanged(t *testing.T) {
	body := `{"id":"msg_ok","type":"message","role":"assistant","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`
	gin.SetMode(gin.TestMode)

	t.Run("passthrough", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		usage, err := newAnthropicEmptyFailoverService().handleNonStreamingResponseAnthropicAPIKeyPassthrough(
			context.Background(), anthropicJSONResponse(body), c, &Account{ID: 50, Platform: PlatformAnthropic},
		)
		require.NoError(t, err)
		require.Equal(t, 5, usage.InputTokens)
		require.Equal(t, 3, usage.OutputTokens)
		require.JSONEq(t, body, rec.Body.String())
	})

	t.Run("messages", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		usage, err := newAnthropicEmptyFailoverService().handleNonStreamingResponse(
			context.Background(), anthropicJSONResponse(body), c, &Account{ID: 51, Platform: PlatformAnthropic}, "claude-sonnet-4-5", "claude-sonnet-4-5",
		)
		require.NoError(t, err)
		require.Equal(t, 5, usage.InputTokens)
		require.Equal(t, 3, usage.OutputTokens)
		require.JSONEq(t, body, rec.Body.String())
	})

	t.Run("tool_use", func(t *testing.T) {
		toolBody := `{"id":"msg_tool","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":3}}`
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		usage, err := newAnthropicEmptyFailoverService().handleNonStreamingResponseAnthropicAPIKeyPassthrough(
			context.Background(), anthropicJSONResponse(toolBody), c, &Account{ID: 52, Platform: PlatformAnthropic},
		)
		require.NoError(t, err)
		require.NotNil(t, usage)
		require.Contains(t, rec.Body.String(), `"name":"Bash"`)
	})
}
