//go:build unit && upstreamcrosspriority

// 上游（origin/main）主张「跨优先级 failover」的两条回归锁，与 fork 自有的
// failover 优先级下限（2b5f9b2e9 "fix(openai): keep failover within priority
// layer"，updateOpenAIFailoverPriorityFloor /
// shouldStopOpenAIHTTPFailoverAtLowerPriority）语义直接冲突，二者不能同时为真：
//
//   - 上游语义：P1 账号失败后应继续切到 P2 兜底账号（9910,9910,9911 → 200）。
//   - fork 语义：任一 P1 失败即把 failover 下限钉在 P1，禁止把 P1 的故障外溢到
//     更低优先级的池子（9910,9910 → 502）。
//
// 两侧都是 merge-base 41def4ba0 之后各自新增，日期同为 07-15、作者不同，
// 因此不是「谁取代谁」，而是产品取舍：P1 全挂时要不要让 P2 接住流量。
// 本次 merge 按 yunfeng「连我之前的 commits 一起合并上去」保留 fork 语义为默认，
// 这两条上游用例**不删**，按 task98 3f1218de5 的先例移入 tag 门控文件保留：
// 默认 `-tags unit` 不编译；若判定采纳上游跨优先级语义，则删除 handler 层的
// 优先级下限三函数并把本文件的 tag 改回 `unit` 即可跑通。
//
// 影响面（本次实测，不是推断）：把 fixture 里三个账号的 Priority 全部改成相同值后，
// 下面从 openai_gateway_credential_failover_loop_test.go / openai_responses_failover_cancel_test.go
// 迁入的四条用例全部转绿。即优先级下限的真实代价不止「跨优先级兜底」这一种场景，
// 而是：**任何账号 Priority 不全相同的池子，首次上游错误之后就不再有 failover**
// （首个被选中的号通常就是最高优先级，失败即把地板钉在该层，同层若只有它一个号，
// 后续任何号都被挡）。P1/P2/P3 各一个号的池子 = 完全没有 failover。
//
// 已收口、不在本决策点内的一类：凭证类失败（Stage=account_auth /
// IsCredentialFailure，从未到达上游）已在 handler 里显式排除出地板，
// 理由是该分类由上游 343390057（07-14）引入、不在 fork HEAD 的祖先里，
// 写地板时本仓还没有它 ⇒ 地板不可能是为它写的。
//
// ⚠️ 决策点（= task98 未收口的决策点 E）：需 @yunfeng 裁定。
// 「默认单测绿」不能推断语义等价。

package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponses_APIKeyPassthroughPool5xxRetriesThenExhaustsMaxSwitches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4203)
	accounts := []service.Account{
		{
			ID: 9910, Name: "pool-api-key", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{
				"api_key":                      "sk-pool",
				"base_url":                     "https://api.example.test",
				"pool_mode":                    true,
				"pool_mode_retry_count":        float64(1),
				"pool_mode_retry_status_codes": []any{float64(http.StatusBadGateway)},
			},
			Extra: map[string]any{"openai_passthrough": true},
		},
		{
			ID: 9911, Name: "fallback-api-key", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 2,
			Credentials: map[string]any{
				"api_key":  "sk-fallback",
				"base_url": "https://api.example.test",
			},
			Extra: map[string]any{"openai_passthrough": true},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.MaxAccountSwitches = 1

	accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
	upstream := &openAIHTTPPassthroughFailoverUpstream{}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	gatewaySvc := service.NewOpenAIGatewayService(
		accountRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	h := NewOpenAIGatewayHandler(
		gatewaySvc,
		service.NewConcurrencyService(nil),
		billingCacheSvc,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID: 1803, GroupID: &groupID,
		User:  &service.User{ID: 1703, Status: service.StatusActive},
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1703, Concurrency: 0})

	h.Responses(c)

	require.Equal(t, []int64{9910, 9910, 9911}, upstream.calls())
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, "Upstream service temporarily unavailable", gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestOpenAIResponses_APIKeyPassthroughPoolAuthFailureRetriesThenSwitchesToHealthyAccount(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "401", statusCode: http.StatusUnauthorized},
		{name: "403", statusCode: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			groupID := int64(4203)
			accounts := []service.Account{
				{
					ID: 9910, Name: "pool-api-key", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1,
					Credentials: map[string]any{
						"api_key":                      "sk-pool",
						"base_url":                     "https://api.example.test",
						"pool_mode":                    true,
						"pool_mode_retry_count":        float64(1),
						"pool_mode_retry_status_codes": []any{float64(tt.statusCode)},
					},
					Extra: map[string]any{"openai_passthrough": true},
				},
				{
					ID: 9911, Name: "fallback-api-key", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 2,
					Credentials: map[string]any{
						"api_key":  "sk-fallback",
						"base_url": "https://api.example.test",
					},
					Extra: map[string]any{"openai_passthrough": true},
				},
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.MaxAccountSwitches = 1

			accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
			upstream := &openAIHTTPPassthroughAuthFailoverUpstream{statusCode: tt.statusCode}
			rateLimitSvc := service.NewRateLimitService(accountRepo, nil, cfg, nil, nil)
			billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCacheSvc.Stop)
			gatewaySvc := service.NewOpenAIGatewayService(
				accountRepo,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				cfg,
				nil,
				nil,
				service.NewBillingService(cfg, nil),
				rateLimitSvc,
				billingCacheSvc,
				upstream,
				&service.DeferredService{},
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
			)
			h := NewOpenAIGatewayHandler(
				gatewaySvc,
				service.NewConcurrencyService(nil),
				billingCacheSvc,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
				nil,
				nil,
				nil,
				nil,
				cfg,
			)

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 1803, GroupID: &groupID,
				User:  &service.User{ID: 1703, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1703, Concurrency: 0})

			h.Responses(c)

			require.Equal(t, []int64{9910, 9910, 9911}, upstream.calls())
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "resp_healthy", gjson.GetBytes(rec.Body.Bytes(), "id").String())
		})
	}
}

// ───────────────────────────────────────────────────────────────────────────
// 以下四条同属上述决策点，原位置：
//   openai_gateway_credential_failover_loop_test.go:561-665（b32b815e4 07-14 /
//     ca0d3314c 07-23，Heatherm Huang）
//   openai_responses_failover_cancel_test.go:182-194（a0593b0bf 07-15，shaw）
// 它们断言「上游 429/402/500/520 之后应切到下一个（更低优先级的）账号」，
// 与 fork 的优先级下限互斥。共用的 newGrokCredentialFailoverHandler /
// newOpenAIResponsesFailoverTestHandler 等 helper 仍留在原文件（tag 只有 unit），
// 双 tag 下同包可见，因此这里不复制 helper。
// 采纳上游语义时：把本文件 tag 改回 unit 并删掉 handler 的优先级下限三函数，
// 或把这四条移回原文件。
// ───────────────────────────────────────────────────────────────────────────

func TestResponsesGrok429FailoverIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("first rate limited account selects healthy account", func(t *testing.T) {
		_, repo, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "first_429")
		defer cleanup()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
		req.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, req)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Contains(t, recorder.Body.String(), "resp_healthy")
		require.Equal(t, []int64{801, 802}, upstream.accountHits())
		require.Equal(t, []int64{801}, repo.rateLimitedAccountIDs())
	})

	t.Run("two rate limited accounts stop without sweeping the pool", func(t *testing.T) {
		_, repo, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "all_429")
		defer cleanup()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
		req.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, req)

		require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
		require.Equal(t, []int64{801, 802}, upstream.accountHits())
		require.Equal(t, []int64{801, 802}, repo.rateLimitedAccountIDs())
		require.NotContains(t, recorder.Body.String(), "expired")
		require.NotContains(t, recorder.Body.String(), "healthy-access")
		require.NotContains(t, recorder.Body.String(), "rate limited")
	})
}

func TestResponsesGrok402FailoverCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, repo, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "first_402")
	defer cleanup()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "resp_healthy")
	require.Equal(t, []int64{801, 802}, upstream.accountHits())
	require.Equal(t, []int64{801}, repo.setTempIDs)
	before := repo.selectorCalls()

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"again","stream":false}`))
	secondReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(second, secondReq)

	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, before+1, repo.selectorCalls())
	require.Equal(t, []int64{801, 802, 802}, upstream.accountHits(), "cooldown must exclude the 402 account from later requests")
}

func TestResponsesGrok429FailoverHandlesMixedStatuses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("429 then 500 stops after the bounded followup", func(t *testing.T) {
		_, _, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "mixed_429_500")
		defer cleanup()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
		req.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, req)

		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		require.Equal(t, []int64{801, 802}, upstream.accountHits())
		require.NotContains(t, recorder.Body.String(), "upstream unavailable")
	})

	t.Run("500 then 429 permits one healthy followup", func(t *testing.T) {
		_, _, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "mixed_500_429")
		defer cleanup()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
		req.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, req)

		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Equal(t, []int64{801, 802, 803}, upstream.accountHits())
	})

	t.Run("OAuth 429 then API-key failure cannot bypass the bound", func(t *testing.T) {
		_, _, upstream, router, cleanup := newGrokCredentialFailoverHandler(t, "oauth_429_apikey_500")
		defer cleanup()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewBufferString(`{"model":"grok","input":"hello","stream":false}`))
		req.Header.Set("Content-Type", "application/json")

		router.ServeHTTP(recorder, req)

		require.Equal(t, http.StatusBadGateway, recorder.Code, recorder.Body.String())
		require.Equal(t, []int64{801, 802}, upstream.accountHits())
	})
}

// 守卫：客户端在线时 failover 行为不变——切换到账号 2，两个账号都 520 后按
// 耗尽返回 502。
// TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient 回归
// 守卫：客户端在线时 failover 行为不变——切换到账号 2，两个账号都 520 后按
// 耗尽返回 502。
func TestOpenAIGatewayHandlerResponses_FailoverContinuesForConnectedClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &openAIResponsesFailoverCancelUpstream{}
	handler := newOpenAIResponsesFailoverTestHandler(t, upstream)
	c, rec := newOpenAIResponsesFailoverTestContext(t, nil)

	handler.Responses(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "在线客户端应正常切换账号")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
}
