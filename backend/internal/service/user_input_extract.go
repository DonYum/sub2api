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

	// messageQueueReminderRe 匹配客户端（Claude Code / WorkBuddy）在运行中途将用户插话包裹在
	// `The user sent a new message while you were working:\n...\nAddress this before completing...` 中的结构，提取其中的真实插话。
	messageQueueReminderRe = regexp.MustCompile(`(?is)The user sent a new message while you were working:\s*(.*?)(?:\n+\s*(?:Address this before completing your current task\.|This is how Claude Code surfaces messages the user sends mid-turn[^\n]*)|\s*</system-reminder>|$)`)

	// codexHandoffSummaryTailRe 剥离 Codex 跨模型/跨阶段续接时附带在用户问题后面的思维链与上下文摘要。
	codexHandoffSummaryTailRe = regexp.MustCompile(`(?is)\n*\s*Another language model started to solve this problem and produced a summary of its thinking process\..*$`)

	// codexQuestionReplyBlockRe 匹配 Codex `<send_user_message_question_reply>...</send_user_message_question_reply>` 块。
	codexQuestionReplyBlockRe = regexp.MustCompile(`(?is)<send_user_message_question_reply\b[^>]*>(.*?)</send_user_message_question_reply>`)

	// claudeImageCoordNoteRe 剥离 Claude Code 附加在图片块后的坐标缩放说明 `[Image: original ... to map to original image.]`、`[Request interrupted by user]` 或 Raft 督促回复提示 `[Your previous response had no visible output...]`。
	claudeImageCoordNoteRe = regexp.MustCompile(`(?is)\[Image:\s*original\s+\d+x\d+[^\]]*to map to original image\.\]|\[\s*Request interrupted by user[^\]]*\]|\[\s*Your previous response had no visible output\.[^\]]*\]|(?:^|\n)\s*##\s*My request:\s*\n`)

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
		regexp.MustCompile(`(?is)<session_context\b[^>]*>.*?</session_context>`),
		regexp.MustCompile(`(?is)<recent-messages\b[^>]*>.*?</recent-messages>`),
		regexp.MustCompile(`(?is)<objects-in-play\b[^>]*>.*?</objects-in-play>`),
		regexp.MustCompile(`(?is)<recent-actions\b[^>]*>.*?</recent-actions>`),
		regexp.MustCompile(`(?is)<task-notification\b[^>]*>.*?</task-notification>`),
		regexp.MustCompile(`(?is)<in-app-browser-context\b[^>]*>.*?</in-app-browser-context>`),
	}

	// codexAgentsMdBlockRe 剥离 Codex CLI/Desktop 注入在首条 user 消息中的 `# AGENTS.md instructions ... </INSTRUCTIONS>` 块。
	codexAgentsMdBlockRe = regexp.MustCompile(`(?is)#\s*AGENTS\.md\s+instructions[^\n]*\n+.*?<INSTRUCTIONS>.*?</INSTRUCTIONS>`)

	// raftInboxNoticeBlockRe 剥离 Raft 平台投递的 `[Raft inbox notice: ...]` 提醒块及其紧随的说明文字。
	// 注意：必须匹配独占一行的 `]` 或无换行的单行 `]`，避免跨出 notice 块误吞后续 `New message received:`。
	raftInboxNoticeBlockRe = regexp.MustCompile(`(?im)\[Raft inbox notice:(?:[^\n\]]*\]|[\s\S]*?\n\])(?:\s*\nThese messages have not been read\.[^\n]*)?`)

	// raftMsgHeaderLineRe 匹配 Raft 消息行头（兼容 `raft message read` 的 `[1/8 seq=... msg=<id> ... type=<type>]`
	// 与 `New message received:` 的 `[target=... msg=<id> ... type=<type>]` 格式）。
	raftMsgHeaderLineRe = regexp.MustCompile(`^\s*\[(?:[0-9]+/[0-9]+\s+|target=[^\]]*?\s+)[^\]]*?\bmsg=([0-9a-fA-F-]+)[^\]]*?\btype=([a-zA-Z_]+)[^\]]*\]\s*(.*)$`)

	// raftReadSubcommandRe 匹配命令行中的 `raft ... message read` 调用。
	raftReadSubcommandRe = regexp.MustCompile(`\braft(?:\s+--?[a-zA-Z0-9_-]+(?:=\S+)?)*\s+message\s+read\b`)
	// raftCheckSubcommandRe 匹配命令行中的 `raft ... message check` 调用。
	raftCheckSubcommandRe = regexp.MustCompile(`\braft(?:\s+--?[a-zA-Z0-9_-]+(?:=\S+)?)*\s+message\s+check\b`)
	// raftHistoryFlagRe 匹配翻查历史专用的参数，防止翻历史时重复入库旧消息。
	raftHistoryFlagRe = regexp.MustCompile(`(?:^|\s)(?:--after|--around|--before|--seq)\b`)
)

// UsageUserInputRecord 表示一条已清洗、已脱敏并截断的用户输入记录。
type UsageUserInputRecord struct {
	RequestID       string
	APIKeyID        int64
	UserID          int64
	Model           string
	UserInput       string
	RaftMessages    []ExtractedRaftHumanMessage
	CreatedAt       time.Time
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

// UsageUserInputRecentChecker 是可选扩展接口：用于检查某个 API Key 近期是否已记录过包含指定标记（如 Raft msg=<shortid>）的用户输入，
// 确保同一个 API Key 下同一条 Raft 消息只存一次（跨重启亦生效）。
type UsageUserInputRecentChecker interface {
	FilterUnseenUsageUserInputMarkers(ctx context.Context, apiKeyID int64, since time.Time, markers []string) (map[string]bool, error)
}

// ExtractedRaftHumanMessage 表示从 `raft message read --unread` / `raft message check` 或唤醒消息中解析出的一条人类消息。
type ExtractedRaftHumanMessage struct {
	MsgShortID string
	Formatted  string
}

var (
	globalUserInputStoreMu sync.RWMutex
	globalUserInputStore   UsageUserInputStore
	globalUserInputQueue   chan UsageUserInputRecord
	globalUserInputOnce    sync.Once

	seenRaftMsgMu sync.Mutex
	seenRaftMsgBy map[int64]map[string]time.Time
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
	cleaned, raftMsgs := extractSanitizedUserInputAndRaftMessages(protocol, body)
	if cleaned == "" && len(raftMsgs) == 0 {
		return false
	}
	var clonedRaft []ExtractedRaftHumanMessage
	if len(raftMsgs) > 0 {
		clonedRaft = make([]ExtractedRaftHumanMessage, len(raftMsgs))
		for i, m := range raftMsgs {
			clonedRaft[i] = ExtractedRaftHumanMessage{
				MsgShortID: strings.Clone(m.MsgShortID),
				Formatted:  strings.Clone(m.Formatted),
			}
		}
	}
	rec := UsageUserInputRecord{
		RequestID:    requestID,
		APIKeyID:     apiKeyID,
		UserID:       userID,
		Model:        strings.TrimSpace(model),
		UserInput:    strings.Clone(cleaned),
		RaftMessages: clonedRaft,
		CreatedAt:    time.Now().UTC(),
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
		finalInput := finalizeUsageUserInputForSave(ctx, store, rec)
		if finalInput != "" {
			rec.UserInput = finalInput
			if err := store.SaveUsageUserInput(ctx, rec); err != nil {
				slog.Warn("usage_user_input.save_failed", "request_id", rec.RequestID, "api_key_id", rec.APIKeyID, "error", err)
			}
		}
		cancel()
	}
}

func finalizeUsageUserInputForSave(ctx context.Context, store UsageUserInputStore, rec UsageUserInputRecord) string {
	var parts []string
	if s := strings.TrimSpace(rec.UserInput); s != "" {
		parts = append(parts, s)
	}
	if len(rec.RaftMessages) > 0 {
		unseen := filterUnseenRaftMessagesForAPIKey(ctx, store, rec.APIKeyID, rec.RaftMessages)
		for _, m := range unseen {
			if s := strings.TrimSpace(m.Formatted); s != "" {
				parts = append(parts, s)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return trimRunes(strings.TrimSpace(strings.Join(parts, "\n")), maxExtractedUserInputRunes)
}

func filterUnseenRaftMessagesForAPIKey(ctx context.Context, store UsageUserInputStore, apiKeyID int64, msgs []ExtractedRaftHumanMessage) []ExtractedRaftHumanMessage {
	if len(msgs) == 0 || apiKeyID <= 0 {
		return msgs
	}

	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -userInputRetentionDays)

	// 1. 先过内存去重表与批次内去重，收集尚未在内存命中的候选 marker（"msg=" + shortID）
	var candidates []ExtractedRaftHumanMessage
	var markers []string
	seenInBatch := make(map[string]struct{}, len(msgs))

	seenRaftMsgMu.Lock()
	if seenRaftMsgBy == nil {
		seenRaftMsgBy = make(map[int64]map[string]time.Time)
	}
	keyMap := seenRaftMsgBy[apiKeyID]
	if keyMap == nil {
		keyMap = make(map[string]time.Time)
		seenRaftMsgBy[apiKeyID] = keyMap
	}
	for k, ts := range keyMap {
		if ts.Before(cutoff) {
			delete(keyMap, k)
		}
	}
	for _, m := range msgs {
		shortID := strings.ToLower(strings.TrimSpace(m.MsgShortID))
		if shortID == "" {
			candidates = append(candidates, m)
			continue
		}
		if _, dup := seenInBatch[shortID]; dup {
			continue
		}
		seenInBatch[shortID] = struct{}{}
		if _, exists := keyMap[shortID]; exists {
			continue
		}
		candidates = append(candidates, m)
		markers = append(markers, "msg="+shortID)
	}
	seenRaftMsgMu.Unlock()

	if len(candidates) == 0 {
		return nil
	}

	// 2. 若 Store 实现了 UsageUserInputRecentChecker，则进一步查库去重（防服务重启后重复落库）
	var alreadyInDB map[string]bool
	if checker, ok := store.(UsageUserInputRecentChecker); ok && len(markers) > 0 {
		if existing, err := checker.FilterUnseenUsageUserInputMarkers(ctx, apiKeyID, cutoff, markers); err == nil {
			alreadyInDB = existing
		}
	}

	var kept []ExtractedRaftHumanMessage
	seenRaftMsgMu.Lock()
	keyMap = seenRaftMsgBy[apiKeyID]
	for _, m := range candidates {
		shortID := strings.ToLower(strings.TrimSpace(m.MsgShortID))
		if shortID != "" {
			marker := "msg=" + shortID
			if alreadyInDB != nil && alreadyInDB[marker] {
				keyMap[shortID] = now
				continue
			}
			if _, exists := keyMap[shortID]; exists {
				continue
			}
			keyMap[shortID] = now
		}
		kept = append(kept, m)
	}
	seenRaftMsgMu.Unlock()
	return kept
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
//  2. 先快速检查请求末尾是否为非 tool_result 的 user 文本消息，或最新一轮 `raft message read ... --unread` / `raft message check` 工具返回结果；
//  3. 识别并排除 Coding Agent 后台伴生守护请求（安全审查、会话标题生成、上下文压缩、状态分类、连接探活、Raft inbox notice、worker_report 工单等）；
//  4. 保留 Raft `New message received:` 中同事发给 Agent 的真实指令，以及 Agent 刚通过 `--unread` / `check` 拉取到的 `type=human` 消息；
//  5. 剥离 <system-reminder>、<environment_context> 等内联噪音块，保留同块内的真实用户提问；
//  6. 复用 redactContentModerationSecrets 脱敏敏感凭据，并截断至 4000 字符以内，供异步队列安全传输。
func ExtractSanitizedUserInput(protocol string, body []byte) string {
	cleaned, raftMsgs := extractSanitizedUserInputAndRaftMessages(protocol, body)
	var parts []string
	if cleaned != "" {
		parts = append(parts, cleaned)
	}
	for _, m := range raftMsgs {
		if m.Formatted != "" {
			parts = append(parts, m.Formatted)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return trimRunes(strings.TrimSpace(strings.Join(parts, "\n")), maxExtractedUserInputRunes)
}

func extractSanitizedUserInputAndRaftMessages(protocol string, body []byte) (string, []ExtractedRaftHumanMessage) {
	raw, raftMsgs := extractCleanUserInputTextAndRaftMessages(protocol, body)
	var cleaned string
	if raw != "" {
		redacted := redactContentModerationSecrets(raw)
		cleaned = trimRunes(strings.TrimSpace(redacted), maxExtractedUserInputRunes)
	}
	if len(raftMsgs) > 0 {
		for i := range raftMsgs {
			redacted := redactContentModerationSecrets(raftMsgs[i].Formatted)
			raftMsgs[i].Formatted = trimRunes(strings.TrimSpace(redacted), maxExtractedUserInputRunes)
		}
	}
	return cleaned, raftMsgs
}

func extractCleanUserInputTextAndRaftMessages(protocol string, body []byte) (string, []ExtractedRaftHumanMessage) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return "", nil
	}

	// 第一步（性能优化）：先检查请求末尾是否为有效 user 文本消息。
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

	var cleanedParts []string
	var raftMsgs []ExtractedRaftHumanMessage
	for _, t := range rawTexts {
		cleaned := cleanSingleUserTextSegment(t)
		if cleaned == "" || isSyntheticContinuationPrompt(cleaned) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(cleaned), "[target=") {
			if directRaft := parseDirectWakeupRaftMessages(cleaned); len(directRaft) > 0 {
				raftMsgs = append(raftMsgs, directRaft...)
			}
			continue
		}
		cleanedParts = append(cleanedParts, cleaned)
	}

	// 第二步：检查最新一轮工具返回结果是否为 `raft message read ... --unread` 或 `raft message check`，
	// 若是则从中提取 `type=human` 的人类消息（兼容 Anthropic Messages、OpenAI Responses/Chat、Gemini）。
	toolRaftMsgs := extractTrailingUnreadRaftToolMessages(protocol, body)
	if len(toolRaftMsgs) > 0 {
		raftMsgs = append(raftMsgs, toolRaftMsgs...)
	}

	if len(cleanedParts) == 0 && len(raftMsgs) == 0 {
		return "", nil
	}

	// 第三步：仅对通过前两步的少数候选请求检查 system/instructions 是否为后台守护任务（标题生成、安全审查、摘要压缩等）。
	if isAgentBackgroundHelperRequest(body) {
		return "", nil
	}

	return strings.Join(cleanedParts, "\n"), dedupeExtractedRaftMessagesInSlice(raftMsgs)
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

	// 2. 若是运行中途插入的用户排队消息（The user sent a new message while you were working: ...），提取其中的真实用户插话
	if m := messageQueueReminderRe.FindStringSubmatch(text); len(m) > 1 {
		q := strings.TrimSpace(m[1])
		if q != "" && !isSyntheticContinuationPrompt(q) {
			return q
		}
	}

	// 3. 若是 Codex 交互式问答回复块 <send_user_message_question_reply>...</send_user_message_question_reply>，直接提取 answer 字段
	if m := codexQuestionReplyBlockRe.FindStringSubmatch(text); len(m) > 1 {
		rawJSON := strings.TrimSpace(m[1])
		if gjson.Valid(rawJSON) {
			var answers []string
			res := gjson.Parse(rawJSON)
			if res.IsArray() {
				for _, item := range res.Array() {
					if ans := strings.TrimSpace(item.Get("answer").String()); ans != "" {
						answers = append(answers, ans)
					}
				}
			} else if ans := strings.TrimSpace(res.Get("answer").String()); ans != "" {
				answers = append(answers, ans)
			}
			if len(answers) > 0 {
				return strings.Join(answers, "\n")
			}
		}
	}

	// 4. 剥离 Codex AGENTS.md 指令块、跨模型交接摘要尾部、图片坐标说明、Raft inbox notice 提醒块与各类 Agent 注入的闭合标签块
	cleaned := codexAgentsMdBlockRe.ReplaceAllString(text, "")
	cleaned = codexHandoffSummaryTailRe.ReplaceAllString(cleaned, "")
	cleaned = raftInboxNoticeBlockRe.ReplaceAllString(cleaned, "")
	for _, re := range strippedAgentNoiseBlockPatterns {
		cleaned = re.ReplaceAllString(cleaned, "")
	}
	cleaned = claudeImageCoordNoteRe.ReplaceAllString(cleaned, "")
	cleaned = imageLocalPathBlockRe.ReplaceAllString(cleaned, "")
	cleaned = extractRaftDeliveredDirectMessages(strings.TrimSpace(cleaned))
	return strings.TrimSpace(cleaned)
}

// extractRaftDeliveredDirectMessages 处理 Raft `New message received:` 唤醒负载：
// 剥离前后的 `[Raft thread context...]` 与 `Respond as appropriate...` 模板包装，
// 直接保留同事在 Raft 中发送的 `[target=... msg=...] @user: ...` 消息正文。
func extractRaftDeliveredDirectMessages(text string) string {
	if !strings.Contains(text, "New message received:") &&
		!strings.Contains(text, "New messages received:") &&
		!strings.HasPrefix(text, "[target=") {
		return text
	}
	lines := strings.Split(text, "\n")
	var kept []string
	capturing := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 只捕获顶层（非 `- [msg=...]` 引用行、非 `│` 缩进引用行）的 `[target=...]` 实际投递消息
		if strings.HasPrefix(line, "[target=") || (strings.HasPrefix(trimmed, "[target=") && !strings.HasPrefix(trimmed, "- ") && !strings.Contains(line, "│")) {
			capturing = true
			kept = append(kept, line)
			continue
		}
		if capturing {
			if strings.HasPrefix(trimmed, "Respond as appropriate.") ||
				strings.HasPrefix(trimmed, "Reply in the channel or create/reply") ||
				strings.HasPrefix(trimmed, "Some unread channels may not be included") ||
				strings.HasPrefix(trimmed, "Run `raft inbox check`") {
				capturing = false
				continue
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
	// Claude Code / Gemini CLI / Codex / WorkBuddy 后台安全审查、会话摘要、标题生成、记忆整理、自动压缩、多 Agent 内部编排唤醒等非人类输入模板
	for _, prefix := range []string{
		"the following is the user's claude.md configuration.",
		"the following is the full conversation transcript",
		"the following is the codex agent history",
		"the user stepped away and is coming back. recap in under",
		"<fork-boilerplate>",
		"summarize the user's primary intent or goal in this conversation in one sentence",
		"you are a helpful assistant. you will be presented with a user prompt, and your job is to provide a short title",
		"generate a concise, single-line task title",
		"you are coming up with a succinct title and git branch name for a coding session",
		"[suggestion mode: suggest what the user might naturally type next",
		"you are performing a context checkpoint compaction. create a handoff summary",
		"your task is to create a detailed and highly structured summary of the conversation so far",
		"analyze this rollout and produce json with `raw_memory`, `rollout_summary`, and `rollout_slug`",
		"## memory writing agent:",
		"output token limit hit. resume directly — no apology, no recap of what you were doing.",
		"time sampled while preparing turn ",
		"agentteams automatic task assignment from the shared task list.",
		"agentteams message from member ",
		"background subagent ",
		"[important: you are running as a scheduled cron job.",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if (strings.HasPrefix(lower, "<session>") && strings.Contains(lower, "write the title in the predominant language")) ||
		strings.Contains(lower, "generate 0 to 3 hyperpersonalized suggestions for what this user can do with codex") ||
		(strings.HasPrefix(lower, "image \"") && strings.Contains(lower, "normalized copy (read-only; may be resized or re-encoded):")) ||
		(strings.HasPrefix(lower, "agent ") && strings.Contains(lower, " sent a message:")) ||
		(strings.HasPrefix(lower, "background job ") && strings.Contains(lower, "status:")) ||
		strings.Contains(trimmed, "现在由哨兵在后台无人值守唤醒") ||
		strings.HasPrefix(trimmed, "<recent-messages ") ||
		strings.HasPrefix(trimmed, "<session_context>") {
		return true
	}
	if strings.Contains(trimmed, "Respond with <severity>N</severity> ONLY") ||
		strings.Contains(trimmed, "Your ENTIRE response MUST begin with <block>") ||
		strings.Contains(trimmed, ">>> APPROVAL REQUEST") {
		return true
	}
	return false
}

// extractTrailingUnreadRaftToolMessages 严格按 4 条规则从最新一轮工具返回结果中提取 Raft 人类消息：
//  1. 只看本次请求里最新一轮的工具返回结果（不扫描更早轮次的历史工具结果）；
//  2. 往前匹配该工具返回对应的命令调用，必须是 `raft message read ... --unread` 或 `raft message check`（含 `--after`、`--around` 或不带 `--unread` 的 read 一律跳过）；
//  3. 在命令返回结果里只取 `type=human` 的消息（连同下方 `│` 开头的续行，忽略 `type=agent` 和 `type=system`）；
//  4. 格式化输出包含发送人与 8 位短消息 ID，供按 `(api_key_id, msg_short_id)` 去重。
func extractTrailingUnreadRaftToolMessages(protocol string, body []byte) []ExtractedRaftHumanMessage {
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		return extractUnreadRaftFromMessagesArray(gjson.GetBytes(body, "messages"), true)
	case ContentModerationProtocolOpenAIChat:
		return extractUnreadRaftFromMessagesArray(gjson.GetBytes(body, "messages"), false)
	case ContentModerationProtocolOpenAIResponses:
		return extractUnreadRaftFromResponsesInput(gjson.GetBytes(body, "input"))
	case ContentModerationProtocolGemini:
		return extractUnreadRaftFromGeminiContents(gjson.GetBytes(body, "contents"))
	default:
		if msgs := extractUnreadRaftFromResponsesInput(gjson.GetBytes(body, "input")); len(msgs) > 0 {
			return msgs
		}
		if msgs := extractUnreadRaftFromMessagesArray(gjson.GetBytes(body, "messages"), true); len(msgs) > 0 {
			return msgs
		}
		return extractUnreadRaftFromGeminiContents(gjson.GetBytes(body, "contents"))
	}
}

func extractUnreadRaftFromMessagesArray(messages gjson.Result, skipTrailingSystem bool) []ExtractedRaftHumanMessage {
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
	if end < 0 {
		return nil
	}

	lastRole := strings.ToLower(strings.TrimSpace(arr[end].Get("role").String()))

	// 场景 A：OpenAI Chat Completions 末尾为连续 `role="tool"` 消息
	if lastRole == "tool" {
		start := end
		for start > 0 && strings.ToLower(strings.TrimSpace(arr[start-1].Get("role").String())) == "tool" {
			start--
		}
		cmdByID := collectToolCommandsFromMessages(arr[:start])
		var out []ExtractedRaftHumanMessage
		for i := start; i <= end; i++ {
			callID := strings.TrimSpace(arr[i].Get("tool_call_id").String())
			if callID == "" || !isUnreadRaftReadOrCheckCommand(cmdByID[callID]) {
				continue
			}
			var texts []string
			collectPlainTexts(arr[i].Get("content"), &texts)
			out = append(out, parseRaftHumanMessagesFromToolOutput(strings.Join(texts, "\n"), false)...)
		}
		return out
	}

	// 场景 B：Anthropic Messages 末尾 `role="user"` 消息内含 `type="tool_result"` 块
	if lastRole == "user" {
		content := arr[end].Get("content")
		if !content.IsArray() {
			return nil
		}
		blocks := content.Array()
		var toolResults []gjson.Result
		for _, b := range blocks {
			if strings.ToLower(strings.TrimSpace(b.Get("type").String())) == "tool_result" {
				toolResults = append(toolResults, b)
			}
		}
		if len(toolResults) == 0 {
			return nil
		}
		cmdByID := collectToolCommandsFromMessages(arr[:end])
		var out []ExtractedRaftHumanMessage
		for _, tr := range toolResults {
			toolUseID := strings.TrimSpace(tr.Get("tool_use_id").String())
			if toolUseID == "" || !isUnreadRaftReadOrCheckCommand(cmdByID[toolUseID]) {
				continue
			}
			var texts []string
			collectPlainTexts(tr.Get("content"), &texts)
			out = append(out, parseRaftHumanMessagesFromToolOutput(strings.Join(texts, "\n"), false)...)
		}
		return out
	}

	return nil
}

func collectToolCommandsFromMessages(prior []gjson.Result) map[string]string {
	out := make(map[string]string)
	for i := len(prior) - 1; i >= 0; i-- {
		m := prior[i]
		if strings.ToLower(strings.TrimSpace(m.Get("role").String())) != "assistant" {
			continue
		}
		// Anthropic `content` array with `type="tool_use"`
		if c := m.Get("content"); c.IsArray() {
			for _, item := range c.Array() {
				if strings.ToLower(strings.TrimSpace(item.Get("type").String())) == "tool_use" {
					id := strings.TrimSpace(item.Get("id").String())
					cmd := extractCommandStringFromToolInput(item.Get("input"))
					if id != "" && cmd != "" {
						out[id] = cmd
					}
				}
			}
		}
		// OpenAI Chat `tool_calls` array
		if tcs := m.Get("tool_calls"); tcs.IsArray() {
			for _, tc := range tcs.Array() {
				id := strings.TrimSpace(tc.Get("id").String())
				cmd := extractCommandStringFromToolInput(tc.Get("function.arguments"))
				if id != "" && cmd != "" {
					out[id] = cmd
				}
			}
		}
	}
	return out
}

func extractUnreadRaftFromResponsesInput(input gjson.Result) []ExtractedRaftHumanMessage {
	if !input.IsArray() {
		return nil
	}
	arr := input.Array()
	if len(arr) == 0 {
		return nil
	}
	end := len(arr) - 1
	if !isResponsesToolOutputItem(arr[end]) {
		return nil
	}
	start := end
	for start > 0 && isResponsesToolOutputItem(arr[start-1]) {
		start--
	}

	cmdByCallID := make(map[string]string)
	for i := 0; i < start; i++ {
		item := arr[i]
		typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if typ == "function_call" || typ == "custom_tool_call" {
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID == "" {
				callID = strings.TrimSpace(item.Get("id").String())
			}
			cmd := extractCommandStringFromToolInput(item.Get("arguments"))
			if cmd == "" {
				cmd = extractCommandStringFromToolInput(item.Get("input"))
			}
			if callID != "" && cmd != "" {
				cmdByCallID[callID] = cmd
			}
		}
	}

	var out []ExtractedRaftHumanMessage
	for i := start; i <= end; i++ {
		item := arr[i]
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" || !isUnreadRaftReadOrCheckCommand(cmdByCallID[callID]) {
			continue
		}
		var texts []string
		collectPlainTexts(item.Get("output"), &texts)
		out = append(out, parseRaftHumanMessagesFromToolOutput(strings.Join(texts, "\n"), false)...)
	}
	return out
}

func isResponsesToolOutputItem(item gjson.Result) bool {
	typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
	return typ == "function_call_output" || typ == "custom_tool_call_output"
}

func extractUnreadRaftFromGeminiContents(contents gjson.Result) []ExtractedRaftHumanMessage {
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

	var funcResponses []gjson.Result
	for _, p := range parts.Array() {
		if fr := p.Get("functionResponse"); fr.Exists() {
			funcResponses = append(funcResponses, fr)
		}
	}
	if len(funcResponses) == 0 {
		return nil
	}

	// 从紧邻的上一轮 model parts 中收集 functionCall 命令
	var modelCmds []string
	for i := len(arr) - 2; i >= 0; i-- {
		r := strings.ToLower(strings.TrimSpace(arr[i].Get("role").String()))
		if r == "model" || r == "assistant" {
			if mParts := arr[i].Get("parts"); mParts.IsArray() {
				for _, mp := range mParts.Array() {
					if fc := mp.Get("functionCall"); fc.Exists() {
						if cmd := extractCommandStringFromToolInput(fc.Get("args")); cmd != "" {
							modelCmds = append(modelCmds, cmd)
						}
					}
				}
			}
			break
		}
	}

	var out []ExtractedRaftHumanMessage
	for idx, fr := range funcResponses {
		matchedCmd := ""
		if idx < len(modelCmds) {
			matchedCmd = modelCmds[idx]
		} else if len(modelCmds) == 1 {
			matchedCmd = modelCmds[0]
		}
		if !isUnreadRaftReadOrCheckCommand(matchedCmd) {
			continue
		}
		var texts []string
		collectPlainTexts(fr.Get("response"), &texts)
		out = append(out, parseRaftHumanMessagesFromToolOutput(strings.Join(texts, "\n"), false)...)
	}
	return out
}

func extractCommandStringFromToolInput(val gjson.Result) string {
	if !val.Exists() {
		return ""
	}
	if val.Type == gjson.String {
		raw := strings.TrimSpace(val.String())
		if strings.HasPrefix(raw, "{") && gjson.Valid(raw) {
			return extractCommandStringFromToolInput(gjson.Parse(raw))
		}
		return raw
	}
	if val.IsObject() {
		for _, key := range []string{"command", "cmd"} {
			v := val.Get(key)
			if v.Type == gjson.String {
				return strings.TrimSpace(v.String())
			}
			if v.IsArray() {
				var parts []string
				for _, item := range v.Array() {
					if s := strings.TrimSpace(item.String()); s != "" {
						parts = append(parts, s)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, " ")
				}
			}
		}
	}
	return ""
}

// isUnreadRaftReadOrCheckCommand 判断命令是否为读取新消息的 `raft message read ... --unread` 或 `raft message check`。
// 任何带有 `--after`、`--around`、`--before`、`--seq` 或不带 `--unread` 的 `raft message read` 均视为翻历史，返回 false。
func isUnreadRaftReadOrCheckCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || !strings.Contains(cmd, "raft") {
		return false
	}
	segments := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == '\n' || r == ';' || r == '&' || r == '|'
	})
	hasValidUnreadCmd := false
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" || !strings.Contains(seg, "raft") {
			continue
		}
		if strings.Contains(seg, "--help") || strings.Contains(seg, " -h") {
			continue
		}
		if raftCheckSubcommandRe.MatchString(seg) {
			hasValidUnreadCmd = true
			continue
		}
		if raftReadSubcommandRe.MatchString(seg) {
			if raftHistoryFlagRe.MatchString(seg) {
				return false
			}
			if strings.Contains(seg, "--unread") {
				hasValidUnreadCmd = true
			}
		}
	}
	return hasValidUnreadCmd
}

func parseDirectWakeupRaftMessages(text string) []ExtractedRaftHumanMessage {
	if !strings.HasPrefix(strings.TrimSpace(text), "[target=") {
		return nil
	}
	return parseRaftHumanMessagesFromToolOutput(text, true)
}

func parseRaftHumanMessagesFromToolOutput(output string, keepFullHeader bool) []ExtractedRaftHumanMessage {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	// 若工具输出是 JSON 包装（如 `{"output":"..."}`），先解包
	trimmedOut := strings.TrimSpace(output)
	if strings.HasPrefix(trimmedOut, "{") && gjson.Valid(trimmedOut) {
		var inner []string
		collectPlainTexts(gjson.Parse(trimmedOut).Get("output"), &inner)
		if len(inner) == 0 {
			collectPlainTexts(gjson.Parse(trimmedOut).Get("content"), &inner)
		}
		if len(inner) > 0 {
			output = strings.Join(inner, "\n")
		}
	}

	lines := strings.Split(output, "\n")
	var results []ExtractedRaftHumanMessage
	var curMsgID string
	var curHeaderLine string
	var curSenderAndFirstLine string
	var curContLines []string
	inHuman := false

	flushCurrent := func() {
		if !inHuman {
			return
		}
		shortID := normalizeRaftMsgShortID(curMsgID)
		var formatted string
		if keepFullHeader {
			all := []string{curHeaderLine}
			all = append(all, curContLines...)
			formatted = strings.TrimSpace(strings.Join(all, "\n"))
		} else {
			body := normalizeRaftSenderBody(curSenderAndFirstLine)
			if len(curContLines) > 0 {
				var cleanedCont []string
				for _, cl := range curContLines {
					cleanedCont = append(cleanedCont, stripRaftContinuationPrefix(cl))
				}
				if body != "" {
					body = body + "\n" + strings.Join(cleanedCont, "\n")
				} else {
					body = strings.Join(cleanedCont, "\n")
				}
			}
			body = strings.TrimSpace(body)
			if body != "" {
				if shortID != "" {
					formatted = "[msg=" + shortID + "] " + body
				} else {
					formatted = body
				}
			}
		}
		if formatted != "" {
			results = append(results, ExtractedRaftHumanMessage{
				MsgShortID: shortID,
				Formatted:  formatted,
			})
		}
		inHuman = false
		curMsgID = ""
		curHeaderLine = ""
		curSenderAndFirstLine = ""
		curContLines = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过引用上下文中的 `- [msg=...]` 列表项
		if strings.HasPrefix(trimmed, "- [") {
			continue
		}
		if m := raftMsgHeaderLineRe.FindStringSubmatch(line); len(m) == 4 {
			flushCurrent()
			msgID := strings.TrimSpace(m[1])
			msgType := strings.ToLower(strings.TrimSpace(m[2]))
			rest := strings.TrimSpace(m[3])
			if msgType == "human" {
				inHuman = true
				curMsgID = msgID
				curHeaderLine = strings.TrimRight(line, "\r")
				curSenderAndFirstLine = rest
			}
			continue
		}
		if inHuman {
			if isRaftContinuationLine(line) {
				curContLines = append(curContLines, strings.TrimRight(line, "\r"))
				continue
			}
			if keepFullHeader && !isRaftFooterLine(trimmed) && trimmed != "" {
				curContLines = append(curContLines, strings.TrimRight(line, "\r"))
				continue
			}
			flushCurrent()
		}
	}
	flushCurrent()
	return dedupeExtractedRaftMessagesInSlice(results)
}

func isRaftContinuationLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "│")
}

func stripRaftContinuationPrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "│") {
		trimmed = strings.TrimPrefix(trimmed, "│")
		return strings.TrimPrefix(trimmed, " ")
	}
	return trimmed
}

func isRaftFooterLine(trimmed string) bool {
	for _, prefix := range []string{
		"Read position:",
		"No more unread",
		"End of window:",
		"Unread window:",
		"Read window:",
		"Coverage:",
		"Server unread cursor",
		"Respond as appropriate.",
		"Reply in the channel",
		"Some unread channels",
		"Run `raft inbox check`",
	} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// normalizeRaftSenderBody 将 `@yunfeng (Asia/Shanghai): 你觉得呢` 简化为 `@yunfeng: 你觉得呢`。
func normalizeRaftSenderBody(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "@") {
		return s
	}
	colonIdx := strings.Index(s, ":")
	if colonIdx <= 1 {
		return s
	}
	senderPart := strings.TrimSpace(s[:colonIdx])
	if parenIdx := strings.Index(senderPart, " ("); parenIdx > 1 && strings.HasSuffix(senderPart, ")") {
		senderPart = strings.TrimSpace(senderPart[:parenIdx])
	}
	return senderPart + ": " + strings.TrimSpace(s[colonIdx+1:])
}

func normalizeRaftMsgShortID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func dedupeExtractedRaftMessagesInSlice(msgs []ExtractedRaftHumanMessage) []ExtractedRaftHumanMessage {
	if len(msgs) <= 1 {
		return msgs
	}
	seen := make(map[string]struct{}, len(msgs))
	out := make([]ExtractedRaftHumanMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.MsgShortID != "" {
			if _, ok := seen[m.MsgShortID]; ok {
				continue
			}
			seen[m.MsgShortID] = struct{}{}
		}
		out = append(out, m)
	}
	return out
}
