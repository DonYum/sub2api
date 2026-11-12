package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	anthropicEmptyOrRefusalErrorCode = "anthropic_empty_or_refusal"
	anthropicRefusalFailoverMessage  = "Anthropic upstream returned stop_reason=refusal"
	anthropicEmptyEndTurnMessage     = "Anthropic upstream returned an empty message with stop_reason=end_turn and no assistant output"
	anthropicEmptyOrRefusalClientMsg = "Upstream returned a content-filter refusal or an empty completion; no fallback account was available"
)

// anthropicMessagesFailoverDetector decides whether an Anthropic Messages
// response is a content-filter refusal or a successful close with no assistant
// output. Usage and stop_details.explanation are not assistant output: Kiro
// can finish HTTP 200 with stop_reason=refusal (and an explanation) or with
// stop_reason=end_turn and only empty text.
//
// Sticky sessions do not block the failover this error triggers. The handler
// adds the failed account to FailedAccountIDs, and both the load-aware and
// legacy schedulers skip a sticky account that is in that exclusion set, so
// the same request can move to another eligible account. Refusal and empty
// end_turn are account+model specific for Claude/Kiro, unlike OpenAI/Codex
// where the same session is expected to stay on one account.
type anthropicMessagesFailoverDetector struct {
	sawText     bool
	sawTool     bool
	sawThinking bool
	sawError    bool
	stopReason  string
}

func newAnthropicMessagesFailoverDetector() *anthropicMessagesFailoverDetector {
	return &anthropicMessagesFailoverDetector{}
}

func (d *anthropicMessagesFailoverDetector) ObserveSSELine(line string) {
	if d == nil {
		return
	}
	if data, ok := extractAnthropicSSEDataLine(line); ok {
		d.ObservePayload([]byte(data))
		return
	}
	trimmed := strings.TrimSpace(line)
	if len(trimmed) >= len("event:") && strings.EqualFold(trimmed[:len("event:")], "event:") {
		name := strings.TrimSpace(trimmed[len("event:"):])
		if strings.EqualFold(name, "error") {
			d.sawError = true
		}
	}
}

func (d *anthropicMessagesFailoverDetector) ObservePayload(payload []byte) {
	if d == nil {
		return
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}

	eventType := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if strings.EqualFold(eventType, "error") {
		d.sawError = true
	}

	// message_start carries message.stop_reason=null while the turn is open.
	// Only the terminal stop_reason (top-level JSON, or message_delta.delta)
	// decides refusal / empty end_turn.
	d.observeStopReason(gjson.GetBytes(payload, "stop_reason").String())
	d.observeStopReason(gjson.GetBytes(payload, "delta.stop_reason").String())

	d.observeContentValue(gjson.GetBytes(payload, "content"))
	d.observeContentValue(gjson.GetBytes(payload, "message.content"))
	if block := gjson.GetBytes(payload, "content_block"); block.Exists() && block.IsObject() {
		d.observeContentBlock(block)
	}
	if delta := gjson.GetBytes(payload, "delta"); delta.Exists() && delta.IsObject() {
		d.observeDelta(delta)
	}
}

func (d *anthropicMessagesFailoverDetector) observeStopReason(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" || strings.EqualFold(reason, "null") {
		return
	}
	d.stopReason = strings.ToLower(reason)
}

func (d *anthropicMessagesFailoverDetector) observeContentValue(content gjson.Result) {
	if !content.Exists() {
		return
	}
	switch content.Type {
	case gjson.String:
		if strings.TrimSpace(content.String()) != "" {
			d.sawText = true
		}
	case gjson.JSON:
		if !content.IsArray() {
			return
		}
		for _, block := range content.Array() {
			d.observeContentBlock(block)
		}
	}
}

func (d *anthropicMessagesFailoverDetector) observeContentBlock(block gjson.Result) {
	if !block.Exists() {
		return
	}
	switch strings.TrimSpace(block.Get("type").String()) {
	case "text":
		if strings.TrimSpace(block.Get("text").String()) != "" {
			d.sawText = true
		}
	case "thinking", "reasoning":
		if strings.TrimSpace(block.Get("thinking").String()) != "" || strings.TrimSpace(block.Get("text").String()) != "" {
			d.sawThinking = true
		}
	case "redacted_thinking":
		if strings.TrimSpace(block.Get("data").String()) != "" {
			d.sawThinking = true
		}
	case "":
		return
	default:
		// tool_use, server_tool_use, mcp_tool_use, and any other non-text
		// block are assistant output even when the input payload is empty.
		d.sawTool = true
	}
}

func (d *anthropicMessagesFailoverDetector) observeDelta(delta gjson.Result) {
	switch strings.TrimSpace(delta.Get("type").String()) {
	case "text_delta":
		if strings.TrimSpace(delta.Get("text").String()) != "" {
			d.sawText = true
		}
	case "thinking_delta", "reasoning_delta", "reasoning_summary_delta":
		if strings.TrimSpace(delta.Get("thinking").String()) != "" || strings.TrimSpace(delta.Get("text").String()) != "" {
			d.sawThinking = true
		}
	case "input_json_delta":
		d.sawTool = true
	case "signature_delta":
		// A signature without thinking text is not user-visible output.
	default:
		if strings.TrimSpace(delta.Get("text").String()) != "" || strings.TrimSpace(delta.Get("thinking").String()) != "" {
			d.sawText = true
		}
	}
}

func (d *anthropicMessagesFailoverDetector) hasAssistantOutput() bool {
	return d != nil && (d.sawText || d.sawTool || d.sawThinking)
}

// ShouldFailover reports whether the response must not be returned as success.
// stop_reason=refusal failovers even when stop_details carries an explanation
// or the message also has assistant text, as long as the caller has not
// written to the client yet. Empty end_turn / stop_sequence failover only
// when there was no text, tool_use, or thinking/reasoning content.
func (d *anthropicMessagesFailoverDetector) ShouldFailover() bool {
	if d == nil || d.sawError {
		return false
	}
	if d.stopReason == "refusal" {
		return true
	}
	if d.hasAssistantOutput() {
		return false
	}
	switch d.stopReason {
	case "end_turn", "stop_sequence":
		return true
	default:
		return false
	}
}

// ShouldReleaseClientOutput is false while the turn might still be an empty
// or refusal close. The first real assistant output (or an upstream error
// event) releases the buffered SSE so the rest of the stream is unchanged.
func (d *anthropicMessagesFailoverDetector) ShouldReleaseClientOutput() bool {
	if d == nil {
		return true
	}
	if d.sawError {
		return true
	}
	if d.ShouldFailover() {
		return false
	}
	return d.hasAssistantOutput()
}

func (s *GatewayService) newAnthropicEmptyOrRefusalFailoverError(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	detector *anthropicMessagesFailoverDetector,
) *UpstreamFailoverError {
	stopReason := ""
	if detector != nil {
		stopReason = detector.stopReason
	}
	message := anthropicEmptyEndTurnMessage
	switch stopReason {
	case "refusal":
		message = anthropicRefusalFailoverMessage
	case "", "end_turn":
	default:
		message = "Anthropic upstream returned an empty message with stop_reason=" + stopReason + " and no assistant output"
	}

	accountID := int64(0)
	accountName := ""
	platform := PlatformAnthropic
	if account != nil {
		accountID = account.ID
		accountName = account.Name
		if strings.TrimSpace(account.Platform) != "" {
			platform = account.Platform
		}
	}
	requestID := ""
	if resp != nil {
		requestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	}

	passthrough := false
	if c != nil {
		if v, ok := c.Get("anthropic_passthrough"); ok {
			passthrough, _ = v.(bool)
		}
	}
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           platform,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  requestID,
		Passthrough:        passthrough,
		Kind:               "failover",
		Message:            message,
	})
	logger.LegacyPrintf("service.gateway", "[Anthropic passthrough] empty/refusal failover: account=%d stop_reason=%s request_id=%s", accountID, stopReason, requestID)

	headers := http.Header{}
	if requestID != "" {
		headers.Set("x-request-id", requestID)
	}
	// Same-account retry cannot clear an account+model content restriction.
	// Leave RetryableOnSameAccount false so the handler switches accounts.
	return &UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    anthropicEmptyOrRefusalErrorBody(message),
		ResponseHeaders: headers,
	}
}

func newAnthropicPassthroughStreamReadFailoverError(err error) *UpstreamFailoverError {
	disconnectMsg := "upstream stream disconnected: " + sanitizeStreamError(err)
	body, marshalErr := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "upstream_disconnected",
			"message": disconnectMsg,
		},
	})
	if marshalErr != nil {
		body = []byte(`{"type":"error","error":{"type":"upstream_disconnected","message":"upstream stream disconnected"}}`)
	}
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           body,
		RetryableOnSameAccount: true,
	}
}

func anthropicEmptyOrRefusalErrorBody(message string) []byte {
	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "upstream_error",
			"code":    anthropicEmptyOrRefusalErrorCode,
			"message": message,
		},
	})
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"upstream_error","code":"anthropic_empty_or_refusal","message":"Anthropic upstream returned an empty or refusal completion"}}`)
	}
	return body
}

// IsAnthropicEmptyOrRefusalErrorBody reports whether a failover body was
// produced for an Anthropic refusal or empty successful close.
func IsAnthropicEmptyOrRefusalErrorBody(body []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) == anthropicEmptyOrRefusalErrorCode
}

// AnthropicEmptyOrRefusalClientMessage is the exhausted-failover client message
// when every eligible account returned a refusal or an empty completion.
func AnthropicEmptyOrRefusalClientMessage() string {
	return anthropicEmptyOrRefusalClientMsg
}
