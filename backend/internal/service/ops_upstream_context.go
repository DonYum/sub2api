package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Gin context keys used by Ops error logger for capturing upstream error details.
// These keys are set by gateway services and consumed by handler/ops_error_logger.go.
const (
	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"
	OpsUpstreamModelKey        = "ops_upstream_model"

	// Optional stage latencies (milliseconds) for troubleshooting and alerting.
	OpsAuthLatencyMsKey      = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey   = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey  = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey  = "ops_response_latency_ms"
	OpsTimeToFirstTokenMsKey = "ops_time_to_first_token_ms"
	// OpenAI WS 关键观测字段
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey 由 applyErrorPassthroughRule 在命中 skip_monitoring=true 的规则时设置。
	// ops_error_logger 中间件检查此 key，为 true 时跳过错误记录。
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// Client-side configuration denials should remain visible in ops_error_logs,
	// but should be excluded from SLA/error-rate calculations.
	// ResponseCommittedKey 由 handleErrorResponse 系列函数在写完 HTTP 错误响应后设置。
	// ensureForwardErrorResponse 检查此 key，为 true 时跳过兜底写入，避免在已完成的 JSON 后追加 SSE。
	ResponseCommittedKey = "response_committed"

	OpsClientBusinessLimitedKey                          = "ops_client_business_limited"
	OpsClientBusinessLimitedReasonKey                    = "ops_client_business_limited_reason"
	OpsClientBusinessLimitedReasonIPRestriction          = "api_key_ip_restriction"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable = "api_key_group_unavailable"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned  = "api_key_group_unassigned"
	OpsClientBusinessLimitedReasonLocalFeatureGate       = "local_feature_gate"
	OpsClientBusinessLimitedReasonLocalPolicyDenied      = "local_policy_denied"
)

func SetOpsUpstreamModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	if model = strings.TrimSpace(model); model != "" {
		c.Set(OpsUpstreamModelKey, model)
	}
}

func MarkResponseCommitted(c *gin.Context) { c.Set(ResponseCommittedKey, true) }

func IsResponseCommitted(c *gin.Context) bool {
	v, ok := c.Get(ResponseCommittedKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

func MarkOpsClientBusinessLimited(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientBusinessLimitedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientBusinessLimitedReasonKey, reason)
	}
}

func HasOpsClientBusinessLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientBusinessLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

// SetOpsUpstreamError is the exported wrapper for setOpsUpstreamError, used by
// handler-layer code (e.g. failover-exhausted paths) that needs to record the
// original upstream status code before mapping it to a client-facing code.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	setOpsUpstreamError(c, upstreamStatusCode, upstreamMessage, upstreamDetail)
}

func setOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// OpsUpstreamErrorEvent describes one upstream error attempt during a single gateway request.
// It is stored in ops_error_logs.upstream_errors as a JSON array.
type OpsUpstreamErrorEvent struct {
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough 表示本次请求是否命中“原样透传（仅替换认证）”分支。
	// 该字段用于排障与灰度评估；存入 JSON，不涉及 DB schema 变更。
	Passthrough bool `json:"passthrough,omitempty"`

	// Context
	Platform    string `json:"platform,omitempty"`
	AccountID   int64  `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`

	// Outcome
	UpstreamStatusCode int    `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`

	// UpstreamURL is the actual upstream URL that was called (host + path, query/fragment stripped).
	// Helps debug 404/routing errors by showing which endpoint was targeted.
	UpstreamURL string `json:"upstream_url,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind: http_error | request_error | retry_exhausted | failover
	Kind string `json:"kind,omitempty"`

	Message           string `json:"message,omitempty"`
	ProviderErrorCode string `json:"provider_error_code,omitempty"`
	ProviderErrorType string `json:"provider_error_type,omitempty"`
	Detail            string `json:"detail,omitempty"`

	SemanticOutputStarted bool   `json:"semantic_output_started,omitempty"`
	PreludeFlushed        bool   `json:"prelude_flushed,omitempty"`
	SafeToFailover        bool   `json:"safe_to_failover,omitempty"`
	AttemptInputTokens    int    `json:"attempt_input_tokens,omitempty"`
	AttemptOutputTokens   int    `json:"attempt_output_tokens,omitempty"`
	AttemptOutcome        string `json:"attempt_outcome,omitempty"`
}

func sanitizeUpstreamErrorIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == ':' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func appendOpsUpstreamError(c *gin.Context, ev OpsUpstreamErrorEvent) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.UpstreamURL = strings.TrimSpace(ev.UpstreamURL)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.ProviderErrorCode = sanitizeUpstreamErrorIdentifier(ev.ProviderErrorCode)
	ev.ProviderErrorType = sanitizeUpstreamErrorIdentifier(ev.ProviderErrorType)
	ev.AttemptOutcome = sanitizeUpstreamErrorIdentifier(ev.AttemptOutcome)
	ev.Detail = strings.TrimSpace(ev.Detail)
	if ev.Message != "" {
		ev.Message = sanitizeUpstreamErrorMessage(ev.Message)
	}

	var existing []*OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

func markLastOpenAICapacityFailoverAttempt(
	c *gin.Context,
	semanticOutputStarted bool,
	preludeFlushed bool,
	usage *OpenAIUsage,
) {
	if c == nil {
		return
	}
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return
	}
	events, ok := v.([]*OpsUpstreamErrorEvent)
	if !ok || len(events) == 0 || events[len(events)-1] == nil {
		return
	}
	event := events[len(events)-1]
	event.SemanticOutputStarted = semanticOutputStarted
	event.PreludeFlushed = preludeFlushed
	event.SafeToFailover = true
	event.AttemptOutcome = "retrying"
	if usage != nil {
		event.AttemptInputTokens = usage.InputTokens
		event.AttemptOutputTokens = usage.OutputTokens
	}
}

// MarkOpenAICapacityFailoverOutcome finalizes all capacity attempts attached to
// the request after the handler knows whether the transparent retry recovered.
func MarkOpenAICapacityFailoverOutcome(c *gin.Context, outcome string) {
	if c == nil {
		return
	}
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return
	}
	events, ok := v.([]*OpsUpstreamErrorEvent)
	if !ok {
		return
	}
	outcome = sanitizeUpstreamErrorIdentifier(outcome)
	for _, event := range events {
		if event != nil && event.SafeToFailover {
			event.AttemptOutcome = outcome
		}
	}
}

// checkSkipMonitoringForUpstreamEvent checks whether the upstream error event
// matches a passthrough rule with skip_monitoring=true and, if so, sets the
// OpsSkipPassthroughKey on the context.  This ensures intermediate retry /
// failover errors (which never go through the final applyErrorPassthroughRule
// path) can still suppress ops_error_logs recording.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := getBoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		c.Set(OpsSkipPassthroughKey, true)
	}
}

func marshalOpsUpstreamErrors(events []*OpsUpstreamErrorEvent) *string {
	if len(events) == 0 {
		return nil
	}
	// Ensure we always store a valid JSON value.
	raw, err := json.Marshal(events)
	if err != nil || len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}

func ParseOpsUpstreamErrors(raw string) ([]*OpsUpstreamErrorEvent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*OpsUpstreamErrorEvent{}, nil
	}
	var out []*OpsUpstreamErrorEvent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveOpsProviderErrorFields promotes provider code/type from the newest
// useful attempt. Detail accepts either a JSON object or a JSON-encoded string.
func ResolveOpsProviderErrorFields(events []*OpsUpstreamErrorEvent) (code, errorType string, parseFailed bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event == nil {
			continue
		}
		code = sanitizeUpstreamErrorIdentifier(event.ProviderErrorCode)
		errorType = sanitizeUpstreamErrorIdentifier(event.ProviderErrorType)
		if code != "" || errorType != "" {
			return code, errorType, false
		}
		for _, raw := range []string{event.Detail, event.UpstreamResponseBody} {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			parsedCode, parsedType, ok := parseOpsProviderErrorJSON(raw)
			if !ok {
				parseFailed = true
				continue
			}
			code = sanitizeUpstreamErrorIdentifier(parsedCode)
			errorType = sanitizeUpstreamErrorIdentifier(parsedType)
			if code != "" || errorType != "" {
				return code, errorType, parseFailed
			}
		}
	}
	return "", "", parseFailed
}

func parseOpsProviderErrorJSON(raw string) (code, errorType string, ok bool) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", "", false
	}
	for depth := 0; depth < 2; depth++ {
		encoded, isString := value.(string)
		if !isString {
			break
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(encoded)), &value); err != nil {
			return "", "", false
		}
	}
	root, isObject := value.(map[string]any)
	if !isObject {
		return "", "", true
	}
	if response, ok := root["response"].(map[string]any); ok {
		if providerErr, ok := response["error"].(map[string]any); ok {
			code, _ = providerErr["code"].(string)
			errorType, _ = providerErr["type"].(string)
		}
	}
	if code == "" && errorType == "" {
		if providerErr, ok := root["error"].(map[string]any); ok {
			code, _ = providerErr["code"].(string)
			errorType, _ = providerErr["type"].(string)
		}
	}
	return code, errorType, true
}

// safeUpstreamURL returns scheme + host + path from a URL, stripping query/fragment
// to avoid leaking sensitive query parameters (e.g. OAuth tokens).
func safeUpstreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.IndexByte(rawURL, '#'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}
