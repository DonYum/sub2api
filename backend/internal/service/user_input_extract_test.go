package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestExtractSanitizedUserInput_ComprehensiveCases(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		body     string
		want     string
	}{
		// --- 1. 正向提取用例（覆盖各协议与各主流 Coding Agent） ---
		{
			name:     "Anthropic_PlainUserQuestion",
			protocol: ContentModerationProtocolAnthropicMessages,
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"先更新再合并提交","cache_control":{"type":"ephemeral"}}]}]}`,
			want:     "先更新再合并提交",
		},
		{
			name:     "Anthropic_TrailingSystemTokenCounterSkipped",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":[{"type":"text","text":"提交并推到远端分支"}]},
					{"role":"system","content":[{"type":"text","text":"<total_tokens>14994494 tokens left</total_tokens>"}]}
				]
			}`,
			want: "提交并推到远端分支",
		},
		{
			name:     "Anthropic_InlineSystemReminderInSameTextBlock",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"<system-reminder>\nAs you answer the user's questions, follow CLAUDE.md.\n</system-reminder>\n帮我重构一下登录鉴权中间件"}
				]
			}`,
			want: "帮我重构一下登录鉴权中间件",
		},
		{
			name:     "Anthropic_SeparateSystemReminderAndMultiLineMeetingNotes",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":[
						{"type":"text","text":"<system-reminder>\n# claudeMd\nCodebase instructions...\n</system-reminder>"},
						{"type":"text","text":"今天要开周会 主要聊几个事情\n1. 事项交接\n2. 语雀评审记录"}
					]}
				]
			}`,
			want: "今天要开周会 主要聊几个事情\n1. 事项交接\n2. 语雀评审记录",
		},
		{
			name:     "Anthropic_UserTextAlongsideToolResultInSameTurn",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":[
						{"type":"tool_result","tool_use_id":"toolu_1","content":"command output..."},
						{"type":"text","text":"顺便把单测也跑一下"}
					]}
				]
			}`,
			want: "顺便把单测也跑一下",
		},
		{
			name:     "OpenAIResponses_CodexConsecutiveUserMessagesWithEnvAndAgentsMd",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\n\n<INSTRUCTIONS>\nShared rules\n</INSTRUCTIONS>"}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <current_date>2026-11-06</current_date>\n  <cwd>/app</cwd>\n</environment_context>"}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"补吧\n"}]}
				]
			}`,
			want: "补吧",
		},
		{
			name:     "OpenAIResponses_URLAndSecretRedaction",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context><cwd>/work</cwd></environment_context>"}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"我的命令是 wget -q \"https://internal.example.com/pkg.tgz\" 且 key 是 sk-proj-1234567890abcdefghijklmnop"}]}
				]
			}`,
			want: `我的命令是 wget -q "[已脱敏]" 且 key 是 [已脱敏]`,
		},
		{
			name:     "OpenAIChat_WorkBuddyUserQueryAndImageStrip",
			protocol: ContentModerationProtocolOpenAIChat,
			body: `{
				"messages":[
					{"role":"user","content":[
						{"type":"text","text":"<system-reminder data-role=\"user-context\">\n<user_info>OS: darwin</user_info>\n<identity_context>SOUL.md</identity_context>\n</system-reminder>\n<user_query>@image#1:Clipboard_Screenshot.png 解读安慰剂检验结果图</user_query>"},
						{"type":"text","text":"<user_query><image_local_path>/Users/maggie/.workbuddy/clipboard.jpg</image_local_path></user_query>"}
					]}
				]
			}`,
			want: "@image#1:Clipboard_Screenshot.png 解读安慰剂检验结果图",
		},
		{
			name:     "OpenAIChat_WorkBuddyCraftModeStrippedAndLongFeedbackKept",
			protocol: ContentModerationProtocolOpenAIChat,
			body: `{
				"messages":[
					{"role":"user","content":[
						{"type":"text","text":"<system-reminder data-role=\"user-context\"><additional_data><current_time>Friday</current_time></additional_data><craft_mode>You are now in Agent mode.</craft_mode><memory_and_skills_reminder>skills</memory_and_skills_reminder></system-reminder>\n<user_query>不要手动模式了，只有自动模式，但是需要用户设置轮次</user_query>"}
					]}
				]
			}`,
			want: "不要手动模式了，只有自动模式，但是需要用户设置轮次",
		},
		{
			name:     "Raft_NewMessageReceivedPreservesColleagueCommandAndStripsEnvelope",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"New message received:\n\n[Raft thread context: you were mentioned in a thread]\nparent: #token_proxy\n\n[target=#token_proxy:dc0ac427 msg=fa8bfe2a time=2026-11-06 07:55:33Z type=human] @alice: 把 v1.6 发到线上\n\nRespond as appropriate. Complete all your work before stopping.\n[Raft inbox notice:\nInbox update: 1 message pending\n]\nThese messages have not been read."}
				]
			}`,
			want: "[target=#token_proxy:dc0ac427 msg=fa8bfe2a time=2026-11-06 07:55:33Z type=human] @alice: 把 v1.6 发到线上",
		},
		{
			name:     "UserNormalQuestionStartingWithCurrentStateWorkingNotFalsePositive",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"Current state: working (for 5m) 这个提示语在哪个文件里定义的？帮我改一下文案"}
				]
			}`,
			want: "Current state: working (for 5m) 这个提示语在哪个文件里定义的？帮我改一下文案",
		},
		{
			name:     "Gemini_GenerateContentUserParts",
			protocol: ContentModerationProtocolGemini,
			body: `{
				"contents":[
					{"role":"user","parts":[{"text":"分析一下这段 Go GC trace 日志"}]}
				]
			}`,
			want: "分析一下这段 Go GC trace 日志",
		},
		{
			name:     "OpenAIImages_PromptExtracted",
			protocol: ContentModerationProtocolOpenAIImages,
			body:     `{"prompt":"画一张极简风格的架构拓扑图"}`,
			want:     "画一张极简风格的架构拓扑图",
		},

		// --- 2. 负向过滤用例（工具循环与各类后台伴生守护请求均返回空） ---
		{
			name:     "Skip_AnthropicToolResultLoop",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"查一下日志"},
					{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"log line 1"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_AnthropicToolResultFollowedByTrailingSystemTokenCount",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"fails=0/30"}]},
					{"role":"system","content":[{"type":"text","text":"<total_tokens>14994494 tokens left</total_tokens>"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_OpenAIResponsesFunctionCallOutputLoop",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"跑测试"}]},
					{"type":"function_call_output","call_id":"c1","output":"PASS"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_OpenAIResponsesCustomToolCallOutputLoop",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"跑测试"}]},
					{"type":"custom_tool_call_output","call_id":"c1","output":"PASS"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_OpenAIChatToolRoleLoop",
			protocol: ContentModerationProtocolOpenAIChat,
			body: `{
				"messages":[
					{"role":"user","content":"查数据库"},
					{"role":"tool","tool_call_id":"c1","content":"0 rows"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_ClaudeCodeSecurityMonitorGuard",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"system":[{"type":"text","text":"You are a security monitor for autonomous AI coding agents."}],
				"messages":[
					{"role":"user","content":"The following is the user's CLAUDE.md configuration."},
					{"role":"user","content":"<transcript>{\"Bash\":\"git status\"}</transcript>\nRespond with <severity>N</severity> ONLY."}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_CodexActionApprovalGuard",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are judging one planned coding-agent action."}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"The following is the Codex agent history...\n>>> APPROVAL REQUEST END"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_SessionTitleGenerator",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"system":[{"type":"text","text":"Generate a concise, sentence-case title (3-7 words) that captures the main topic or goal of this coding session."}],
				"messages":[
					{"role":"user","content":"<session>\n今天要开周会\n</session>\nWrite the title in the predominant language"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_ClaudeCodeAwayRecap",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"The user stepped away and is coming back. Recap in under 40 words, 1-2 plain sentences, no markdown."}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_ClaudeCodeBackgroundStateClassifierWithToolCallsSoFar",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"system":[{"type":"text","text":"A user kicked off a Claude Code agent to do a coding task and walked away."}],
				"messages":[
					{"role":"user","content":"Current state: working (for 1m)\nTool calls so far: Bash×215, Edit×28\nUser's most recent ask: \"帮我写文档\""}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_PiContextSummarizationAssistant",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"role":"system","content":"You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant."},
					{"role":"user","content":[{"type":"input_text","text":"<conversation>\n[User]: 画流程图\n</conversation>"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_WorkBuddySummarizationContinuation",
			protocol: ContentModerationProtocolOpenAIChat,
			body: `{
				"messages":[
					{"role":"user","content":"<system-reminder><user_info>darwin</user_info></system-reminder><conversation_history_summary>summary</conversation_history_summary>\n<user_query>Please continue with the conversation based on the summarized context above. Maintain the same level of detail and helpfulness as before the summarization.</user_query>"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_ClaudeCodeSubagentForkBoilerplate",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"<fork-boilerplate>\nYou are a worker fork. The transcript above is the parent's history.\n</fork-boilerplate>\nDo task X"}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_RaftInboxNoticeOnly",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"[Raft inbox notice:\nInbox update: 1 message pending your read\n]\nThese messages have not been read. Choose when to read them: raft message read"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_DeepSeekHarnessWorkerReportJSON",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":[
						{"type":"text","text":"{\"kind\":\"worker_report\",\"ticket\":{\"ticket_id\":\"123\",\"title\":\"任务\"}}"},
						{"type":"text","text":"{\"ticket_ended\":{\"ticket_id\":\"123\",\"state\":\"completed\"}}"}
					]}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_ClaudeCLIQuotaProbe",
			protocol: ContentModerationProtocolAnthropicMessages,
			body:     `{"messages":[{"role":"user","content":"quota"}]}`,
			want:     "",
		},
		{
			name:     "Skip_CodexDesktopHyperpersonalizedSuggestions",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"# Overview\n\nGenerate 0 to 3 hyperpersonalized suggestions for what this user can do with Codex in this local project: /Users/demo"}]}]}`,
			want:     "",
		},
		{
			name:     "ClaudeCode_StripLocalCommandCaveatAndKeepQuestion",
			protocol: ContentModerationProtocolAnthropicMessages,
			body:     `{"messages":[{"role":"user","content":"<local-command-caveat>caveat</local-command-caveat>\n<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>opus</command-args>\n<local-command-stdout>Set model to opus</local-command-stdout>\n集团财务能稳定读到智能记账的结账状态"}]}`,
			want:     "集团财务能稳定读到智能记账的结账状态",
		},
		{
			name:     "GeminiCLI_StripSessionContextAndKeepUserPrompt",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"text":"<session_context>\nThis is the Gemini CLI. We are setting up the context for our chat.\n├── outline_1.md\n</session_context>\n帮我看下这个项目的路由是怎么转发的"}]}]}`,
			want:     "帮我看下这个项目的路由是怎么转发的",
		},
		{
			name:     "Skip_RaftCompactionSummaryInjection",
			protocol: ContentModerationProtocolAnthropicMessages,
			body:     `{"messages":[{"role":"user","content":"<recent-messages order=\"newest last\">[target=#token_proxy]</recent-messages>\n<objects-in-play><object name=\"foo.go\"/></objects-in-play>\n<recent-actions>s1 Bash</recent-actions>"}]}`,
			want:     "",
		},
		{
			name:     "Skip_GeminiCLIConversationSummaryBackgroundJob",
			protocol: ContentModerationProtocolGemini,
			body:     `{"contents":[{"role":"user","parts":[{"text":"Summarize the user's primary intent or goal in this conversation in ONE sentence (max 80 characters).\nFocus on what the user was trying to accomplish.\n\nConversation:\nUser: 111\n\nSummary (max 80 chars):"}]}]}`,
			want:     "",
		},
		{
			name:     "Extract_MidTurnMessageQueueReminderUserInput",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":"<system-reminder data-role=\"message-queue\">\nThe user sent a new message while you were working:\n不能把极端值直接去除吗？即使有少数是真实的，去除也不会太影响整体吧\nAddress this before completing your current task.\n</system-reminder>"}]}`,
			want:     "不能把极端值直接去除吗？即使有少数是真实的，去除也不会太影响整体吧",
		},
		{
			name:     "Extract_CodexQuestionReplyAnswerAndStripHandoffSummary",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"<send_user_message_question_reply>\n[{\"questionItemId\":\"q1\",\"question\":\"是否还有可用于本轮验收的 GPU 集群？\",\"answer\":\"这个用例先跳过 后面我会让你在k8s 11 环境 验证GPU\"}]\n</send_user_message_question_reply>\nAnother language model started to solve this problem and produced a summary of its thinking process. Here is the summary..."}]}]}`,
			want:     "这个用例先跳过 后面我会让你在k8s 11 环境 验证GPU",
		},
		{
			name:     "Skip_CodexContextCheckpointCompaction",
			protocol: ContentModerationProtocolOpenAIResponses,
			body:     `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume the task."}]}]}`,
			want:     "",
		},
		{
			name:     "Skip_TaskNotificationOnly",
			protocol: ContentModerationProtocolOpenAIChat,
			body:     `{"messages":[{"role":"user","content":"<task-notification>\n<task-id>aGyVf2</task-id>\n<status>failed</status>\n</task-notification>"}]}`,
			want:     "",
		},
		{
			name:     "Raft_ThreadContextWithBracketsDoesNotCorruptDeliveredMessage",
			protocol: ContentModerationProtocolAnthropicMessages,
			body:     `{"messages":[{"role":"user","content":"New message received:\n\n[Raft thread context: you were mentioned in a thread without model-visible context.]\nparent: #token_proxy\nthread: #token_proxy:dc0ac427\n\nRecent thread context (truncated):\n- [msg=232878c1 seq=20792183 time=2026-11-06 14:16:34Z type=agent] @gemini: 剥离 [Image: original ...]\n\n[target=#token_proxy:dc0ac427 msg=4cf94ab9 time=2026-11-06 15:20:25Z type=human] @yunfeng (Asia/Shanghai): @gemini 你采集的信息全吗？为啥没有“云风”的？\n\nRespond as appropriate. Complete all your work before stopping."}]}`,
			want:     "[target=#token_proxy:dc0ac427 msg=4cf94ab9 time=2026-11-06 15:20:25Z type=human] @yunfeng (Asia/Shanghai): @gemini 你采集的信息全吗？为啥没有“云风”的？",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractSanitizedUserInput(tc.protocol, []byte(tc.body))
			require.Equal(t, tc.want, got)
		})
	}
}

type memoryUserInputStore struct {
	mu      sync.Mutex
	records map[UsageUserInputKey]string
}

func (s *memoryUserInputStore) SaveUsageUserInput(_ context.Context, rec UsageUserInputRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[UsageUserInputKey]string)
	}
	key := UsageUserInputKey{RequestID: rec.RequestID, APIKeyID: rec.APIKeyID}
	if _, exists := s.records[key]; !exists {
		s.records[key] = rec.UserInput
	}
	return nil
}

func (s *memoryUserInputStore) BatchGetUsageUserInputs(_ context.Context, keys []UsageUserInputKey) (map[UsageUserInputKey]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[UsageUserInputKey]string)
	for _, k := range keys {
		if v, ok := s.records[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (s *memoryUserInputStore) CleanupExpiredUsageUserInputs(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

func TestCaptureAndHydrateUsageUserInput(t *testing.T) {
	store := &memoryUserInputStore{}
	RegisterUsageUserInputStore(store)
	defer RegisterUsageUserInputStore(nil)

	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "cli-req-99")
	body := []byte(`{"messages":[{"role":"user","content":"<system-reminder>ctx</system-reminder>\n排查一下 .64 的慢查询"}]}`)
	ok := CaptureAndEnqueueUsageUserInputFromContext(ctx, 88, 7, ContentModerationProtocolAnthropicMessages, "claude-opus-5-5", body)
	require.True(t, ok)

	expectedKey := UsageUserInputKey{RequestID: "client:cli-req-99", APIKeyID: 88}
	require.Eventually(t, func() bool {
		res, _ := store.BatchGetUsageUserInputs(context.Background(), []UsageUserInputKey{expectedKey})
		return res[expectedKey] == "排查一下 .64 的慢查询"
	}, time.Second, 10*time.Millisecond)

	logs := []UsageLog{
		{ID: 1, RequestID: "client:cli-req-99", APIKeyID: 88},
		{ID: 2, RequestID: "client:missing", APIKeyID: 88},
	}
	svc := &UsageService{}
	svc.hydrateUsageLogUserInputs(context.Background(), logs)
	require.NotNil(t, logs[0].UserInput)
	require.Equal(t, "排查一下 .64 的慢查询", *logs[0].UserInput)
	require.Nil(t, logs[1].UserInput)
}
