package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 判据 7 的行为级锁:透传 + 无显式 effort + 残留 mapping 指向 passback-required 上游。
//
// 为什么需要它:task #87 候选自带的
// TestOpenAIGatewayService_ThinkingFallbackUsesForwardedModelSemantics 只有两例
//   - 透传 + 显式 effort=medium -> medium(显式优先,与本次改动无关)
//   - 非透传 -> high(走 mapped model,本来就该这样)
//
// 都没盖住 "透传 + 无显式 effort + 残留 mapping" 这一例,也就是 :2708 改动存在的
// 唯一理由。把 :2708 改回 ApplyThinkingEnabledFallback(..., account.GetMappedModel(reqModel))
// 后候选四个新用例全部仍 PASS = 改动无锁。本用例在同一 httpUpstream 夹具上跑真
// Forward,变异后 FAIL、候选上 PASS。
func TestT87Lock_PassthroughEffortIgnoresResidualMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 注意:无 "reasoning" 字段 => 无显式 effort,必须靠 fallback 决定
	body := []byte(`{"model":"gpt-5.2","stream":true,"store":false,"instructions":"test","thinking":{"type":"enabled"},"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-t87"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"id":"resp_t87","status":"completed","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	account := &Account{
		ID: 187, Name: "t87-lock", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
			// 残留映射:把 gpt-5.2 指向 passback-required 的 glm-
			"model_mapping": map[string]any{"gpt-5.2": "glm-4.6"},
		},
		Extra: map[string]any{"openai_passthrough": true},
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 上游实际收到的仍是 reqModel(自动透传不改模型)
	require.Equal(t, "gpt-5.2", gjson.GetBytes(upstream.lastBody, "model").String())

	// 锁 1(请求体级):上游请求体里不许出现 reasoning.effort。
	// 这条才是"真进请求体"的那一档 —— 与锁 2 是两件事:effort 即使只进
	// 元数据也已经污染统计/去重键,但只要它进了 body 就变成计费与上游行为
	// 差异。任何未来把 effort 写回 passthrough body 的改动必须让这条转红。
	if eff := gjson.GetBytes(upstream.lastBody, "reasoning.effort"); eff.Exists() {
		t.Fatalf("透传请求体被注入 reasoning.effort=%q(上游会真的收到它)", eff.String())
	}

	// 锁 2(元数据级):result.ReasoningEffort 是计费/统计与去重键的输入。
	if result.ReasoningEffort != nil {
		t.Fatalf("透传下 effort 被残留 mapping 注入: %q (应为 nil)", *result.ReasoningEffort)
	}
}

// 上面那个用例的锁 1 只在 effort 非 nil 时才有判别力,而在 :2708 正确时
// effort 恰好是 nil —— 也就是说单独把 effort 写进 body 这个改动,在那个用例
// 上抓不到。本用例把这一档单独锁住:reqModel 自身就是 passback-required
// (glm-4.6 + thinking enabled),于是 :2708 正确时 effort 也合法地是 "high",
// 但自动透传的契约要求它只进元数据、绝不进上游请求体。
//
// 判别力:在 forwardOpenAIPassthrough 里加一行把 effort 写回 body,本用例
// 转红而上面那个不转。两者合起来覆盖"元数据被污染"和"上游真的收到"两档。
func TestT87Lock_PassthroughNeverWritesEffortIntoUpstreamBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 无显式 reasoning 字段;thinking enabled + passback-required 模型
	// => fallback 合法地给出 effort=high(仅元数据)
	body := []byte(`{"model":"glm-4.6","stream":true,"store":false,"instructions":"test","thinking":{"type":"enabled"},"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-t87b"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.completed","response":{"id":"resp_t87b","status":"completed","model":"glm-4.6","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n"))),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	account := &Account{
		ID: 188, Name: "t87-lock-body", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Concurrency: 1, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
		Extra: map[string]any{"openai_passthrough": true},
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 前提自检:effort 必须非 nil,否则本用例没有判别力(等于空转)。
	require.NotNil(t, result.ReasoningEffort, "前提不成立:effort 为 nil,本用例锁不住任何东西")
	require.Equal(t, "high", *result.ReasoningEffort)

	// 真正的锁:元数据里有 effort,上游请求体里必须没有。
	if eff := gjson.GetBytes(upstream.lastBody, "reasoning.effort"); eff.Exists() {
		t.Fatalf("effort 进了上游请求体: reasoning.effort=%q(应只存在于元数据)", eff.String())
	}
	if eff := gjson.GetBytes(upstream.lastBody, "reasoning_effort"); eff.Exists() {
		t.Fatalf("effort 进了上游请求体: reasoning_effort=%q(应只存在于元数据)", eff.String())
	}
}
