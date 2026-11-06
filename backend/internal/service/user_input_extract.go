package service

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

const (
	maxExtractedUserInputRunes = 4000
	userInputPersistWorkers    = 2
	userInputPersistQueueSize  = 1024
	userInputPersistTimeout    = 5 * time.Second
	userInputRetentionDays     = 30
	userInputCleanupInterval   = 6 * time.Hour
	userInputCleanupDelay      = 5 * time.Minute
)

var (
	// userQueryBlockRe 匹配客户端（如 WorkBuddy / Cursor 等）显式包裹用户真实提问的 <user_query>...</user_query> 标签。
	userQueryBlockRe = regexp.MustCompile(`(?is)<user_query\b[^>]*>(.*?)</user_query>`)

	// imageLocalPathBlockRe 剥离 WorkBuddy 剪贴板图片本地路径附件块。
	imageLocalPathBlockRe = regexp.MustCompile(`(?is)<image_local_path\b[^>]*>.*?</image_local_path>`)

	// strippedAgentNoiseBlockPatterns 剥离各类 Coding Agent 注入在 user 消息中的非用户指令/环境噪音闭合标签块。
	// 仅剥离闭合标签块本身，保留同一文本块中标签以外的用户真实输入（解决 content_moderation_input.go 整块丢弃的盲区）。
	strippedAgentNoiseBlockPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<system-reminder\b[^>]*>.*?</system-reminder>`),
		regexp.MustCompile(`(?is)<environment_context\b[^>]*>.*?</environment_context>`),
		regexp.MustCompile(`(?is)<permissions\s+instructions\b[^>]*>.*?</permissions\s+instructions>`),
		regexp.MustCompile(`(?is)<collaboration_mode\b[^>]*>.*?</collaboration_mode>`),
		regexp.MustCompile(`(?is)<project_context\b[^>]*>.*?</project_context>`),
		regexp.MustCompile(`(?is)<skills\b[^>]*>.*?</skills>`),
		regexp.MustCompile(`(?is)<memory-index\b[^>]*>.*?</memory-index>`),
		regexp.MustCompile(`(?is)<cwd\b[^>]*>.*?</cwd>`),
		regexp.MustCompile(`(?is)<user_info\b[^>]*>.*?</user_info>`),
		regexp.MustCompile(`(?is)<identity_context\b[^>]*>.*?</identity_context>`),
		regexp.MustCompile(`(?is)<additional_data\b[^>]*>.*?</additional_data>`),
		regexp.MustCompile(`(?is)<craft_mode\b[^>]*>.*?</craft_mode>`),
		regexp.MustCompile(`(?is)<memory_and_skills_reminder\b[^>]*>.*?</memory_and_skills_reminder>`),
		regexp.MustCompile(`(?is)<conversation_history_summary\b[^>]*>.*?</conversation_history_summary>`),
		regexp.MustCompile(`(?is)<total_tokens\b[^>]*>.*?</total_tokens>`),
		regexp.MustCompile(`(?is)<local-command-caveat\b[^>]*>.*?</local-command-caveat>`),
		regexp.MustCompile(`(?is)<local-command-stdout\b[^>]*>.*?</local-command-stdout>`),
		regexp.MustCompile(`(?is)<command-name\b[^>]*>.*?</command-name>`),
		regexp.MustCompile(`(?is)<command-message\b[^>]*>.*?</command-message>`),
		regexp.MustCompile(`(?is)<command-args\b[^>]*>.*?</command-args>`),
		regexp.MustCompile(`(?is)<acp\b[^>]*>.*?</acp>`),
	}

	// codexAgentsMdBlockRe 剥离 Codex CLI/Desktop 注入在首条 user 消息中的 `# AGENTS.md instructions ... </INSTRUCTIONS>` 块。
	codexAgentsMdBlockRe = regexp.MustCompile(`(?is)#\s*AGENTS\.md\s+instructions[^\n]*\n+.*?<INSTRUCTIONS>.*?</INSTRUCTIONS>`)

	// raftInboxNoticeBlockRe 剥离 Raft 平台投递的 `[Raft inbox notice: ...]` 提醒块及其紧随的说明文字。
	raftInboxNoticeBlockRe = regexp.MustCompile(`(?is)\[Raft inbox notice:.*?\](?:\s*These messages have not been read\.[^\n]*)?`)
)

// UsageUserInputRecord 表示一条已清洗、已脱敏并截断的用户输入记录。
type UsageUserInputRecord struct {
	RequestID string
	APIKeyID  int64
	UserID    int64
	Model     string
	UserInput string
	CreatedAt time.Time
}

type UsageUserInputKey struct {
	RequestID string
	APIKeyID  int64
}

// UsageUserInputStore 定义用户输入旁路表的持久化与批量查询接口（可选实现，与主 UsageLogRepository 解耦）。
type UsageUserInputStore interface {
	SaveUsageUserInput(ctx context.Context, record UsageUserInputRecord) error
	BatchGetUsageUserInputs(ctx context.Context, keys []UsageUserInputKey) (map[UsageUserInputKey]string, error)
	CleanupExpiredUsageUserInputs(ctx context.Context, before time.Time) (int64, error)
}

var (
	globalUserInputStoreMu sync.RWMutex
	globalUserInputStore   UsageUserInputStore
	globalUserInputQueue   chan UsageUserInputRecord
	globalUserInputOnce    sync.Once
)

// RegisterUsageUserInputStore 注册全局的用户输入异步持久化存储并启动后台 Worker 与定期清理协程。
func RegisterUsageUserInputStore(store UsageUserInputStore) {
	globalUserInputStoreMu.Lock()
	globalUserInputStore = store
	globalUserInputStoreMu.Unlock()
	if store == nil {
		return
	}
	globalUserInputOnce.Do(func() {
		globalUserInputQueue = make(chan UsageUserInputRecord, userInputPersistQueueSize)
		for range userInputPersistWorkers {
			go runUsageUserInputWorker()
		}
		go runUsageUserInputCleanupWorker()
	})
}

func getUsageUserInputStore() UsageUserInputStore {
	globalUserInputStoreMu.RLock()
	defer globalUserInputStoreMu.RUnlock()
	return globalUserInputStore
}

// CaptureAndEnqueueUsageUserInputFromContext 使用与 usage_logs 完全一致的 request_id 规则（resolveUsageBillingRequestID）
// 在请求转发前同步提取、脱敏并截断用户输入，仅将截断后的短字符串投入有界异步队列。
func CaptureAndEnqueueUsageUserInputFromContext(ctx context.Context, apiKeyID, userID int64, protocol, model string, body []byte) bool {
	if ctx == nil || apiKeyID <= 0 || len(body) == 0 {
		return false
	}
	requestID := resolveUsageBillingRequestID(ctx, "")
	return CaptureAndEnqueueUsageUserInput(requestID, apiKeyID, userID, protocol, model, body)
}

// CaptureAndEnqueueUsageUserInput 在请求转发前同步提取、脱敏并截断用户输入，
// 仅将截断后的短字符串（≤4000 字符）投入有界异步队列，绝不在内存中驻留原始请求体。
func CaptureAndEnqueueUsageUserInput(requestID string, apiKeyID, userID int64, protocol, model string, body []byte) bool {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || apiKeyID <= 0 || len(body) == 0 {
		return false
	}
	store := getUsageUserInputStore()
	if store == nil || globalUserInputQueue == nil {
		return false
	}
	cleaned := ExtractSanitizedUserInput(protocol, body)
	if cleaned == "" {
		return false
	}
	rec := UsageUserInputRecord{
		RequestID: requestID,
		APIKeyID:  apiKeyID,
		UserID:    userID,
		Model:     strings.TrimSpace(model),
		UserInput: strings.Clone(cleaned),
		CreatedAt: time.Now().UTC(),
	}
	select {
	case globalUserInputQueue <- rec:
		return true
	default:
		slog.Warn("usage_user_input.queue_full", "request_id", requestID, "api_key_id", apiKeyID)
		return false
	}
}

func runUsageUserInputWorker() {
	for rec := range globalUserInputQueue {
		store := getUsageUserInputStore()
		if store == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), userInputPersistTimeout)
		if err := store.SaveUsageUserInput(ctx, rec); err != nil {
			slog.Warn("usage_user_input.save_failed", "request_id", rec.RequestID, "api_key_id", rec.APIKeyID, "error", err)
		}
		cancel()
	}
}

func runUsageUserInputCleanupWorker() {
	timer := time.NewTimer(userInputCleanupDelay)
	defer timer.Stop()
	for {
		<-timer.C
		if store := getUsageUserInputStore(); store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cutoff := time.Now().UTC().AddDate(0, 0, -userInputRetentionDays)
			if _, err := store.CleanupExpiredUsageUserInputs(ctx, cutoff); err != nil {
				slog.Warn("usage_user_input.cleanup_failed", "error", err)
			}
			cancel()
		}
		timer.Reset(userInputCleanupInterval)
	}
}

// ExtractSanitizedUserInput 从网关请求体中提取当轮纯净的用户输入：
//  1. 与 upstream 的 content_moderation_input.go 完全解耦，不修改任何现有审核逻辑；
//  2. 先快速检查请求末尾是否为非 tool_result 的 user 文本消息（直接短路掉 67%+ 的工具循环请求，避免无谓扫描大段 system 提示词）；
//  3. 识别并排除 Coding Agent 后台伴生守护请求（安全审查、会话标题生成、上下文压缩、状态分类、连接探活、Raft inbox notice、worker_report 工单等）；
//  4. 保留 Raft `New message received:` 中同事发给 Agent 的真实指令；
//  5. 剥离 <system-reminder>、<environment_context> 等内联噪音块，保留同块内的真实用户提问；
//  6. 复用 redactContentModerationSecrets 脱敏敏感凭据，并截断至 4000 字符以内，供异步队列安全传输。
func ExtractSanitizedUserInput(protocol string, body []byte) string {
	raw := extractCleanUserInputText(protocol, body)
	if raw == "" {
		return ""
	}
	redacted := redactContentModerationSecrets(raw)
	return trimRunes(strings.TrimSpace(redacted), maxExtractedUserInputRunes)
}

func extractCleanUserInputText(protocol string, body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}

	// 第一步（性能优化）：先检查请求末尾是否为有效 user 文本消息。
	// 67% 以上的工具循环请求（tool_result / function_call_output / tool / assistant）在此处直接返回，无需扫描庞大的 system 字段。
	var rawTexts []string
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		rawTexts = collectTrailingUserTextsFromMessages(gjson.GetBytes(body, "messages"), true)
	case ContentModerationProtocolOpenAIChat:
		rawTexts = collectTrailingUserTextsFromMessages(gjson.GetBytes(body, "messages"), false)
	case ContentModerationProtocolOpenAIResponses:
		rawTexts = collectTrailingUserTextsFromResponses(gjson.GetBytes(body, "input"))
	case ContentModerationProtocolGemini:
		rawTexts = collectTrailingUserTextsFromGemini(gjson.GetBytes(body, "contents"))
	case ContentModerationProtocolOpenAIImages:
		if p := strings.TrimSpace(gjson.GetBytes(body, "prompt").String()); p != "" {
			rawTexts = []string{p}
		}
	default:
		rawTexts = collectTrailingUserTextsFromResponses(gjson.GetBytes(body, "input"))
		if len(rawTexts) == 0 {
			rawTexts = collectTrailingUserTextsFromMessages(gjson.GetBytes(body, "messages"), true)
		}
		if len(rawTexts) == 0 {
			rawTexts = collectTrailingUserTextsFromGemini(gjson.GetBytes(body, "contents"))
		}
	}

	if len(rawTexts) == 0 {
		return ""
	}

	// 第二步：先清洗末尾 user 文本段并剔除合成/守护提示词。
	var cleanedParts []string
	for _, t := range rawTexts {
		cleaned := cleanSingleUserTextSegment(t)
		if cleaned != "" && !isSyntheticContinuationPrompt(cleaned) {
			cleanedParts = append(cleanedParts, cleaned)
		}
	}
	if len(cleanedParts) == 0 {
		return ""
	}

	// 第三步：仅对通过前两步的少数候选请求检查 system/instructions 是否为后台守护任务（标题生成、安全审查、摘要压缩等）。
	if isAgentBackgroundHelperRequest(body) {
		return ""
	}

	return strings.Join(cleanedParts, "\n")
}

// isAgentBackgroundHelperRequest 识别 Coding Agent 自动发起的后台伴生请求（非人类直接输入）。
func isAgentBackgroundHelperRequest(body []byte) bool {
	sysText := collectSystemAndInstructionText(body)
	if sysText != "" {
		lower := strings.ToLower(sysText)
		for _, marker := range []string{
			"you are a security monitor for autonomous ai coding agents",
			"you are judging one planned coding-agent action",
			"use prior reviews as context, not binding precedent",
			"generate a concise, sentence-case title",
			"a user kicked off a claude code agent to do a coding task and walked away",
			"you are a context summarization assistant",
			"extract any file paths that this command reads or modifies",
		} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

func collectSystemAndInstructionText(body []byte) string {
	var parts []string
	if sys := gjson.GetBytes(body, "system"); sys.Exists() {
		collectPlainTexts(sys, &parts)
	}
	if inst := gjson.GetBytes(body, "instructions"); inst.Exists() {
		collectPlainTexts(inst, &parts)
	}
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		for _, m := range msgs.Array() {
			role := strings.ToLower(strings.TrimSpace(m.Get("role").String()))
			if role == "system" || role == "developer" {
				collectPlainTexts(m.Get("content"), &parts)
			}
		}
	}
	if inp := gjson.GetBytes(body, "input"); inp.IsArray() {
		for _, item := range inp.Array() {
			role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
			if role == "system" || role == "developer" {
				collectPlainTexts(item.Get("content"), &parts)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func collectPlainTexts(val gjson.Result, out *[]string) {
	switch {
	case !val.Exists():
		return
	case val.Type == gjson.String:
		if s := strings.TrimSpace(val.String()); s != "" {
			*out = append(*out, s)
		}
	case val.IsArray():
		for _, item := range val.Array() {
			collectPlainTexts(item, out)
		}
	case val.IsObject():
		if t := val.Get("text"); t.Exists() {
			collectPlainTexts(t, out)
		}
		if c := val.Get("content"); c.Exists() {
			collectPlainTexts(c, out)
		}
	}
}

func collectTrailingUserTextsFromMessages(messages gjson.Result, skipTrailingSystem bool) []string {
	if !messages.IsArray() {
		return nil
	}
	arr := messages.Array()
	if len(arr) == 0 {
		return nil
	}
	end := len(arr) - 1
	if skipTrailingSystem {
		for end >= 0 && strings.ToLower(strings.TrimSpace(arr[end].Get("role").String())) == "system" {
			end--
		}
	}
	if end < 0 || strings.ToLower(strings.TrimSpace(arr[end].Get("role").String())) != "user" {
		return nil
	}
	if messageHasToolResultOnly(arr[end].Get("content")) {
		return nil
	}

	start := end
	for start > 0 && strings.ToLower(strings.TrimSpace(arr[start-1].Get("role").String())) == "user" {
		if messageHasToolResultOnly(arr[start-1].Get("content")) {
			break
		}
		start--
	}

	var texts []string
	for i := start; i <= end; i++ {
		collectUserTextParts(arr[i].Get("content"), &texts)
	}
	return texts
}

func messageHasToolResultOnly(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	hasToolResult := false
	hasUserText := false
	for _, item := range content.Array() {
		typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if typ == "tool_result" {
			hasToolResult = true
		} else if typ == "" || typ == "text" || typ == "input_text" {
			if cleanSingleUserTextSegment(item.Get("text").String()) != "" {
				hasUserText = true
			}
		}
	}
	return hasToolResult && !hasUserText
}

func collectUserTextParts(content gjson.Result, out *[]string) {
	switch {
	case !content.Exists():
		return
	case content.Type == gjson.String:
		if s := strings.TrimSpace(content.String()); s != "" {
			*out = append(*out, s)
		}
	case content.IsArray():
		for _, item := range content.Array() {
			collectUserTextParts(item, out)
		}
	case content.IsObject():
		typ := strings.ToLower(strings.TrimSpace(content.Get("type").String()))
		switch typ {
		case "", "text", "input_text", "message":
			if t := content.Get("text"); t.Exists() && t.Type == gjson.String {
				if s := strings.TrimSpace(t.String()); s != "" {
					*out = append(*out, s)
				}
			}
			if c := content.Get("content"); c.Exists() {
				collectUserTextParts(c, out)
			}
		}
	}
}

func collectTrailingUserTextsFromResponses(input gjson.Result) []string {
	switch {
	case !input.Exists():
		return nil
	case input.Type == gjson.String:
		if s := strings.TrimSpace(input.String()); s != "" {
			return []string{s}
		}
		return nil
	case input.IsObject():
		role := strings.ToLower(strings.TrimSpace(input.Get("role").String()))
		if role != "" && role != "user" {
			return nil
		}
		var out []string
		collectUserTextParts(input.Get("content"), &out)
		if t := input.Get("text"); t.Exists() {
			collectUserTextParts(t, &out)
		}
		return out
	case input.IsArray():
		arr := input.Array()
		if len(arr) == 0 {
			return nil
		}
		end := len(arr) - 1
		if !isResponsesUserItem(arr[end]) {
			return nil
		}
		start := end
		for start > 0 && isResponsesUserItem(arr[start-1]) {
			start--
		}
		var out []string
		for i := start; i <= end; i++ {
			collectUserTextParts(arr[i].Get("content"), &out)
			if arr[i].Get("type").String() == "input_text" && arr[i].Get("text").Exists() {
				collectUserTextParts(arr[i].Get("text"), &out)
			}
		}
		return out
	default:
		return nil
	}
}

func isResponsesUserItem(item gjson.Result) bool {
	typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	if typ == "function_call_output" || typ == "custom_tool_call_output" || typ == "function_call" || typ == "custom_tool_call" || typ == "reasoning" || typ == "agent_message" {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
	return role == "user" || (role == "" && typ == "input_text")
}

func collectTrailingUserTextsFromGemini(contents gjson.Result) []string {
	if !contents.IsArray() {
		return nil
	}
	arr := contents.Array()
	if len(arr) == 0 {
		return nil
	}
	last := arr[len(arr)-1]
	role := strings.ToLower(strings.TrimSpace(last.Get("role").String()))
	if role != "" && role != "user" {
		return nil
	}
	parts := last.Get("parts")
	if !parts.IsArray() {
		return nil
	}
	var out []string
	for _, p := range parts.Array() {
		if p.Get("functionResponse").Exists() || p.Get("functionCall").Exists() {
			continue
		}
		if t := strings.TrimSpace(p.Get("text").String()); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func cleanSingleUserTextSegment(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// 1. 若存在显式的 <user_query>...</user_query> 标签（WorkBuddy 等），优先提取其中的用户提问
	if matches := userQueryBlockRe.FindAllStringSubmatch(text, -1); len(matches) > 0 {
		var queries []string
		for _, m := range matches {
			q := imageLocalPathBlockRe.ReplaceAllString(m[1], "")
			q = strings.TrimSpace(q)
			if q != "" && !isSyntheticContinuationPrompt(q) {
				queries = append(queries, q)
			}
		}
		return strings.Join(queries, "\n")
	}

	// 2. 剥离 Codex AGENTS.md 指令块、Raft inbox notice 提醒块与各类 Agent 注入的闭合标签块
	cleaned := codexAgentsMdBlockRe.ReplaceAllString(text, "")
	cleaned = raftInboxNoticeBlockRe.ReplaceAllString(cleaned, "")
	for _, re := range strippedAgentNoiseBlockPatterns {
		cleaned = re.ReplaceAllString(cleaned, "")
	}
	cleaned = imageLocalPathBlockRe.ReplaceAllString(cleaned, "")
	cleaned = extractRaftDeliveredDirectMessages(strings.TrimSpace(cleaned))
	return strings.TrimSpace(cleaned)
}

// extractRaftDeliveredDirectMessages 处理 Raft `New message received:` 唤醒负载：
// 剥离前后的 `[Raft thread context...]` 与 `Respond as appropriate...` 模板包装，
// 直接保留同事在 Raft 中发送的 `[target=... msg=...] @user: ...` 消息正文。
func extractRaftDeliveredDirectMessages(text string) string {
	if !strings.HasPrefix(text, "New message received:") && !strings.HasPrefix(text, "New messages received:") {
		return text
	}
	lines := strings.Split(text, "\n")
	var kept []string
	capturing := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[target=") {
			capturing = true
			kept = append(kept, line)
			continue
		}
		if capturing {
			if strings.HasPrefix(trimmed, "Respond as appropriate.") ||
				strings.HasPrefix(trimmed, "Reply in the channel or create/reply") ||
				strings.HasPrefix(trimmed, "Some unread channels may not be included") ||
				strings.HasPrefix(trimmed, "Run `raft inbox check`") {
				break
			}
			kept = append(kept, line)
		}
	}
	if len(kept) > 0 {
		return strings.TrimSpace(strings.Join(kept, "\n"))
	}
	return text
}

func isSyntheticContinuationPrompt(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	// Claude CLI 探活 / 空启动词
	if lower == "quota" || lower == "start." || lower == "no new messages while you were away. nothing to do — just stop." {
		return true
	}
	// WorkBuddy 自动续写指令
	if strings.HasPrefix(lower, "please continue with the conversation based on the summarized context above") {
		return true
	}
	// DeepSeek Harness / Agent 间机器工单 JSON（worker_report / ticket_ended）
	if strings.HasPrefix(trimmed, "{") &&
		(strings.Contains(trimmed, `"kind":"worker_report"`) || strings.Contains(trimmed, `"ticket_ended":`)) {
		return true
	}
	// Claude Code 后台状态通知器（需同时包含 Current state 与 Tool calls so far:，避免误伤普通用户提问）
	if strings.HasPrefix(lower, "current state: working (for ") && strings.Contains(lower, "tool calls so far:") {
		return true
	}
	// Claude Code 后台安全审查 / Away Recap / Subagent Fork / 标题生成 / Codex 后台建议生成模板
	for _, prefix := range []string{
		"the following is the user's claude.md configuration.",
		"the following is the full conversation transcript",
		"the following is the codex agent history",
		"the user stepped away and is coming back. recap in under",
		"<fork-boilerplate>",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if (strings.HasPrefix(lower, "<session>") && strings.Contains(lower, "write the title in the predominant language")) ||
		strings.Contains(lower, "generate 0 to 3 hyperpersonalized suggestions for what this user can do with codex") {
		return true
	}
	if strings.Contains(trimmed, "Respond with <severity>N</severity> ONLY") ||
		strings.Contains(trimmed, "Your ENTIRE response MUST begin with <block>") ||
		strings.Contains(trimmed, ">>> APPROVAL REQUEST") {
		return true
	}
	return false
}
