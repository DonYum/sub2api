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
		{
			name:     "RaftTool_Anthropic_UnreadReadExtractsHumanWithContinuationAndIgnoresAgent",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"[Raft inbox notice:\nInbox update: 2 messages pending\n]\nThese messages have not been read."},
					{"role":"assistant","content":[{"type":"tool_use","id":"toolu_read1","name":"Bash","input":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --unread"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read1","content":"Unread window: 3 returned, seq 20814827-20814968, oldest to newest.\n\n[1/3 seq=20814827 msg=41b202d8-484f-42ed-b913-8674b9710e68 time=2026-11-06 18:53:26Z type=human] @yunfeng (Asia/Shanghai): 还是没看到“云风”在raft 里发起的调用信息啊\n  │ 第二行补充说明\n[2/3 seq=20814878 msg=9a9c1c41-7ca0-4d8e-9da2-8904c162b72c time=2026-11-06 18:54:26Z type=agent] @韩立: 更正我之前说的\n  │ - 子项\n[3/3 seq=20814968 msg=1b535781-10da-41e4-9ea4-60bcd91d174c time=2026-11-06 18:55:54Z type=human] @yunfeng (Asia/Shanghai): @韩立 你觉得呢\n\nRead position: seq 20814265 → 20814968."}]}
				]
			}`,
			want: "[msg=41b202d8] @yunfeng: 还是没看到“云风”在raft 里发起的调用信息啊\n第二行补充说明\n[msg=1b535781] @yunfeng: @韩立 你觉得呢",
		},
		{
			name:     "RaftTool_Anthropic_SkipHistoryReadWithAfterOrWithoutUnread",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"toolu_hist","name":"Bash","input":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --after 20788522"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_hist","content":"[1/1 seq=20788523 msg=14b2d241-5eff-4fef-bda2-a73521d1805b time=2026-11-06 13:37:44Z type=human] @yunfeng (Asia/Shanghai): @gemini 继续看上面的问题"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "RaftTool_Anthropic_OnlyLatestToolTurnInspected",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --unread"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"[1/1 seq=1 msg=41b202d8-484f-42ed-b913-8674b9710e68 time=2026-11-06 18:53:26Z type=human] @yunfeng: 第一轮已读消息"}]},
					{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"git status -s"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"M backend/internal/service/user_input_extract.go"}]}
				]
			}`,
			want: "",
		},
		{
			name:     "RaftTool_OpenAIResponses_SlockDaemonShellCommandArrayAndMessageCheck",
			protocol: ContentModerationProtocolOpenAIResponses,
			body: `{
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"[Raft inbox notice:\nInbox update: 1 message pending\n]"}]},
					{"type":"function_call","call_id":"call_raft_1","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"raft message check\"]}"},
					{"type":"function_call_output","call_id":"call_raft_1","output":"{\"output\":\"Exit code: 0\\nOutput:\\n[target=#oppo_watch msg=cb32a0e6 time=2026-11-06 15:54:58Z type=human] @yunfeng (Asia/Shanghai): @韩立 你去找一下有没有合适的开源的项目。\\n[target=#oppo_watch:cb32a0e6 msg=53390d8a time=2026-11-06 15:55:10Z type=agent] @韩立: 收到\"}"}
				]
			}`,
			want: "[msg=cb32a0e6] @yunfeng: @韩立 你去找一下有没有合适的开源的项目。",
		},
		{
			name:     "RaftTool_OpenAIChat_ToolRoleUnreadRead",
			protocol: ContentModerationProtocolOpenAIChat,
			body: `{
				"messages":[
					{"role":"assistant","tool_calls":[{"id":"call_chat_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"raft message read --target \\\"#oppo_watch\\\" --unread\"}"}}]},
					{"role":"tool","tool_call_id":"call_chat_1","content":"[1/1 seq=20801391 msg=cb32a0e6-706e-4b02-a24e-18c2b4e4d619 time=2026-11-06 15:54:58Z type=human] @yunfeng (Asia/Shanghai): @韩立 你去找一下有没有合适的开源的项目。"}
				]
			}`,
			want: "[msg=cb32a0e6] @yunfeng: @韩立 你去找一下有没有合适的开源的项目。",
		},
		{
			name:     "RaftTool_Gemini_FunctionResponseUnreadRead",
			protocol: ContentModerationProtocolGemini,
			body: `{
				"contents":[
					{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --unread"}}}]},
					{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"content":"[1/1 seq=20815202 msg=cdac32ef-d6bb-48d9-b613-4321c2600d42 time=2026-11-06 18:59:47Z type=human] @yunfeng (Asia/Shanghai): 好的，你们改吧，明天早上我想看到你们改好并部署了。"}}}]}
				]
			}`,
			want: "[msg=cdac32ef] @yunfeng: 好的，你们改吧，明天早上我想看到你们改好并部署了。",
		},
		{
			name:     "Skip_RaftWakeupWithAgentMessageOnly",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"New message received:\n\n[Raft thread context: you were mentioned in a thread]\nparent: #token_proxy\n\n[target=#token_proxy:dc0ac427 msg=a44d8159 time=2026-11-06 20:18:02Z type=agent] @韩立: @gemini 审完 00624971b，可以部署\n  │ 顺手修一处小问题\n\nRespond as appropriate. Complete all your work before stopping."}
				]
			}`,
			want: "",
		},
		{
			name:     "Skip_RaftNoVisibleOutputNudgeAndCapitalInboxNotice",
			protocol: ContentModerationProtocolAnthropicMessages,
			body: `{
				"messages":[
					{"role":"user","content":"[Raft Inbox notice:\nApp items pending: 1\nRun ` + "`raft inbox check`" + ` to inspect them.\n]\n[Your previous response had no visible output. Please continue and produce a user-visible response.]"}
				]
			}`,
			want: "",
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

func (s *memoryUserInputStore) FilterUnseenUsageUserInputMarkers(_ context.Context, apiKeyID int64, _ time.Time, markers []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool)
	for k, v := range s.records {
		if k.APIKeyID != apiKeyID {
			continue
		}
		for _, m := range markers {
			if m != "" && containsIgnoreCase(v, m) {
				out[m] = true
			}
		}
	}
	return out, nil
}

func containsIgnoreCase(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || len(haystack) > 0 && (containsFold(haystack, needle)))
}

func containsFold(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestCaptureAndHydrateUsageUserInput(t *testing.T) {
	store := &memoryUserInputStore{}
	seenRaftMsgMu.Lock()
	seenRaftMsgBy = nil
	seenRaftMsgMu.Unlock()
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
}

func TestRaftMessageDeduplicationPerAPIKey(t *testing.T) {
	store := &memoryUserInputStore{}
	seenRaftMsgMu.Lock()
	seenRaftMsgBy = nil
	seenRaftMsgMu.Unlock()
	RegisterUsageUserInputStore(store)
	defer RegisterUsageUserInputStore(nil)

	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --unread"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"[1/2 seq=1 msg=41b202d8-484f-42ed-b913-8674b9710e68 time=2026-11-06 18:53:26Z type=human] @yunfeng (Asia/Shanghai): 第一条提问\n[2/2 seq=2 msg=921a8965-0a32-4365-a514-afc5ba557a69 time=2026-11-06 18:58:37Z type=human] @yunfeng (Asia/Shanghai): 第二条提问"}]}
		]
	}`)

	// 1. Key 6 第一次读取：两条消息都应保存
	ctx1 := context.WithValue(context.Background(), ctxkey.ClientRequestID, "req-key6-1")
	require.True(t, CaptureAndEnqueueUsageUserInputFromContext(ctx1, 6, 1, ContentModerationProtocolAnthropicMessages, "claude-opus-5-5", body))
	key6Req1 := UsageUserInputKey{RequestID: "client:req-key6-1", APIKeyID: 6}
	require.Eventually(t, func() bool {
		res, _ := store.BatchGetUsageUserInputs(context.Background(), []UsageUserInputKey{key6Req1})
		return res[key6Req1] == "[msg=41b202d8] @yunfeng: 第一条提问\n[msg=921a8965] @yunfeng: 第二条提问"
	}, time.Second, 10*time.Millisecond)

	// 2. Key 6 第二次读到同一条 msg=41b202d8 + 一条新消息 msg=cdac32ef：应只存新消息 msg=cdac32ef
	body2 := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"raft message read --target \"#token_proxy:dc0ac427\" --unread"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"[1/2 seq=1 msg=41b202d8-484f-42ed-b913-8674b9710e68 time=2026-11-06 18:53:26Z type=human] @yunfeng (Asia/Shanghai): 第一条提问\n[2/2 seq=3 msg=cdac32ef-d6bb-48d9-b613-4321c2600d42 time=2026-11-06 18:59:47Z type=human] @yunfeng (Asia/Shanghai): 第三条新提问"}]}
		]
	}`)
	ctx2 := context.WithValue(context.Background(), ctxkey.ClientRequestID, "req-key6-2")
	require.True(t, CaptureAndEnqueueUsageUserInputFromContext(ctx2, 6, 1, ContentModerationProtocolAnthropicMessages, "claude-opus-5-5", body2))
	key6Req2 := UsageUserInputKey{RequestID: "client:req-key6-2", APIKeyID: 6}
	require.Eventually(t, func() bool {
		res, _ := store.BatchGetUsageUserInputs(context.Background(), []UsageUserInputKey{key6Req2})
		return res[key6Req2] == "[msg=cdac32ef] @yunfeng: 第三条新提问"
	}, time.Second, 10*time.Millisecond)

	// 3. 另一个 API Key（Key 9，例如 `云风-gpt`）读到相同的第一批消息：不同 Key 互不影响，应完整保存
	ctx3 := context.WithValue(context.Background(), ctxkey.ClientRequestID, "req-key9-1")
	require.True(t, CaptureAndEnqueueUsageUserInputFromContext(ctx3, 9, 1, ContentModerationProtocolAnthropicMessages, "gpt-6-astra", body))
	key9Req1 := UsageUserInputKey{RequestID: "client:req-key9-1", APIKeyID: 9}
	require.Eventually(t, func() bool {
		res, _ := store.BatchGetUsageUserInputs(context.Background(), []UsageUserInputKey{key9Req1})
		return res[key9Req1] == "[msg=41b202d8] @yunfeng: 第一条提问\n[msg=921a8965] @yunfeng: 第二条提问"
	}, time.Second, 10*time.Millisecond)
}
