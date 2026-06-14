# sub2api 后续优化分析

> 对应 #token_proxy task #14（2026-06-15）。本文只分析当前
> `DonYum/sub2api` 自维护分支还能优化什么，不包含本次未验证的代码改动。

## 结论先行

当前 `sub2api` 的主链路已经具备可生产运营的基础能力：多账号调度、sticky session、账单去重、
Kiro credits 计费、Kiro 折扣展示、OpenAI / Anthropic / Gemini / Antigravity 多入口都已接入。

下一阶段最有价值的优化不是继续扩协议面，而是围绕"赚钱模型"补四类基础设施：

1. **成本与账单正确性**：任何漏记、重记、错价都会直接影响利润和客户信任。
2. **账号池规模化维护**：刷新、健康、quota、调度状态要从被动撞错转成主动可见。
3. **热路径与后台写压力**：并发队列、scheduler outbox、token refresh 在账号/请求规模变大后会成为瓶颈。
4. **可观测与归因闭环**：让单条请求能从客户、API key、业务标签、sub2api usage row、上游账号和 kiro.rs trace 串起来。

因此建议优先级是 P0/P1 基础设施，而不是插件化、大 UI、新模型功能。

## 当前基线

- 本地分支基线：`origin/main@b9c69bde`。
- 与 upstream：`origin/main` 比 `upstream/main@e34ad2b1` 领先 18 个提交，当前没有落后 upstream/main。
- 已包含近期关键修复：
  - Kiro credits 记录、按 credits 计费、折扣率展示。
  - sticky 不再绑定最低优先级 fallback 账号。
  - upstream #3252 zstd 响应解压。
  - upstream #3195 `invalid_refresh_token` / `app_session_terminated` 非重试。
  - upstream #3250 保留 Anthropic window cooldown。
  - upstream #3154 Responses `input` sticky hash fallback。
- 本地未跟踪目录：`backend/artifacts/`、`backend/backend/`，本次未触碰。
- 生产即时 SSH 抽样被权限挡住：`root@10.12.0.64` 返回 `Permission denied (publickey,...)`。本文不把新的生产状态写成已验证事实。

## 已确认的本地代码观察

### 1. `scheduler_outbox` 仍是时间窗口去重，且没有消费后清理

当前 `backend/internal/repository/scheduler_outbox_repo.go`：

- `schedulerOutboxDedupWindow = time.Second`。
- 去重依赖 `created_at >= NOW() - 1s` 的扫描。
- worker 用 Redis watermark 消费 `id > watermark`。
- watermark 成功写入后没有删除 `id <= watermark` 的已消费行。

这意味着账号状态变化频率上来后，`scheduler_outbox` 会长期膨胀，并且入队去重会越来越依赖表扫描和索引质量。

### 2. `token_refresh` 仍扫描全部 active 账号

当前 `backend/internal/service/token_refresh_service.go`：

- `listActiveAccounts()` 直接调用 `accountRepo.ListActive(ctx)`。
- 之后在 Go 内逐个判断平台、账号类型、refresh token、是否进入刷新窗口。
- refresh retry exhausted 后虽然会设置 10 分钟临时不可调度，但下一轮候选仍会先把该账号取回。
- 刷新失败路径仍会尝试 OpenAI / Antigravity privacy 外部调用。

账号规模上来后，这会把"少数即将过期 OAuth 账号"的问题放大成"每轮全量 active 账号扫描"。

### 3. 用户等待队列仍在热路径外层计数

多个 handler 仍先调用 `IncrementWaitCount`，再调用 `AcquireUserSlotWithWait`：

- `backend/internal/handler/gateway_handler.go`
- `backend/internal/handler/gateway_handler_chat_completions.go`
- `backend/internal/handler/gateway_handler_responses.go`
- `backend/internal/handler/gemini_v1beta_handler.go`
- `backend/internal/handler/openai_gateway_handler.go`

这使立即拿到用户并发槽位的请求也会额外打 Redis 等待队列计数。高 QPS 下这是纯热路径成本。

### 4. usage_logs 有 request_id / client_request_id 基础，但缺业务 metadata

当前已有：

- `X-Client-Request-ID` 中间件。
- request_id / client_request_id 进入 ops 监控和部分 usage billing 指纹。
- usage log 有 API key、user、group、account、model、endpoint、request_type 等维度。

但 `usage_logs` 还没有 caller-supplied metadata。对代理商或大客户来说，一个 API key 内部的终端用户、
项目、功能、订单无法直接在账单中归因，后续客服和利润分析会继续依赖外部对账。

### 5. group 使用汇总存在已标注的全表扫描风险

`backend/internal/repository/usage_log_repo.go` 已有 TODO：

- `GetAllGroupUsageSummary` 会扫描 `usage_logs` 全表做累计成本。
- 代码注释建议超过约 100 万行后考虑缓存、物化视图或预聚合表。

usage_logs 是核心增长表，这个风险会随着运营成功必然放大。

## P0：账单正确性回归与成本异常监控

### 问题

过去几天已经连续暴露账单语义问题：

- Kiro 真实成本在 `meteringEvent credits`，但标准 cache token 恒为 0，导致按满 input tokens 计费会多收。
- Kiro savings 展示分母曾经出现 `$15/$75` 与官方 `$5/$25` 口径争议。
- OpenAI 图片相关 upstream PR 显示图像 input/output token、false image billing 都容易造成多收或少记。

这类问题不是普通 bug。它们直接影响：

- 客户是否被多收。
- 平台实际毛利是否被高估。
- API key 代理商能否信任账单。

### 建议

先补"账单校验闭环"，不要等客户报错：

- 增加每日账单异常报表：
  - `actual_cost < 0`、`total_cost < 0`。
  - `actual_cost = 0` 但 `input_tokens + output_tokens > 0`。
  - Kiro 请求 `upstream_kiro_credits IS NULL` 的比例。
  - Kiro 请求 `total_cost != upstream_kiro_credits * multiplier` 的比例。
  - image billing 请求中 `image_count > 0` 但 token/size 字段异常。
- 对 Kiro 单独加统计：
  - credits 缺失率。
  - `kiro_discount_rate_estimate` p50/p90/p99。
  - 负 savings 被 clamp 的比例。
- 给账单计算加 golden cases：
  - Kiro credits。
  - OpenAI image false-positive。
  - image input token 单独计价。
  - image output token 不重复进入 text output。
  - context-window / non-failover 错误不产生成功 usage。

### 可借鉴 upstream PR

- #3215：修复 text-only `/v1/responses` 被误判为图片输出导致按图收费。
- #3234：修复 image output tokens 在 account stats 中被 text output 重复计价。
- #3235：image input tokens 单独计价。

这些 PR 不建议盲合。建议先按当前自维护分支的计费模型抽取最小账单测试和必要字段，避免和现有 Kiro credits / 渠道定价逻辑冲突。

### 成功标准

- 能每天回答"昨天是否有明显多收/少收/漏记风险"。
- Kiro 链路 credits 缺失率可见。
- 图像计费不再依赖人工看 usage 明细猜测。
- 账单相关 PR 必须带至少一条 end-to-end usage log 断言。

## P0：scheduler_outbox 去重、清理与写压力治理

### 问题

调度缓存是请求选账号的基础设施。当前 outbox 仍存在三个风险：

- 已消费历史行不清理，表会持续膨胀。
- 1 秒时间窗口去重在高频状态变更下不稳定，且仍可能产生大量写入。
- `SetTempUnschedulable` 等路径即使没有实际状态变化，也可能写 outbox，放大调度重建。

近期 Kiro / OpenAI / Antigravity 链路都会频繁触发账号状态变化：限流、quota、refresh 失败、临时不可调度、账号恢复。
账号量和请求量上去后，这个表会从"后台实现细节"变成实际瓶颈。

### 建议

分两步做，先稳再快：

1. 消费后清理：
   - watermark 成功写入 Redis 后，按小批量删除 `id <= watermark` 的行。
   - 多实例用 PostgreSQL advisory lock。
   - 每批限制 5000，避免大事务和 WAL 膨胀。
2. 去重语义升级：
   - 增加 `dedup_key`。
   - 用 pending partial unique index 替代时间窗口扫描。
   - worker claim 时释放 pending dedup key，避免 in-flight 期间吞掉后续真实状态变更。
   - `SetTempUnschedulable` 仅在 `RowsAffected() > 0` 时写 outbox。

### 可借鉴 upstream PR

- #3274：已消费 outbox 清理，改动小，适合优先拆入。
- #3255：dedup_key + pending unique index，效果更完整，但涉及迁移和 worker 语义，需单独 review。

### 成功标准

- `scheduler_outbox` 总行数稳定，不随时间单调增长。
- 高频 account_changed 下 DB CPU / outbox 写入量下降。
- 不能丢状态变更：同 key 事件在 in-flight 后仍可再次入队。
- 不影响 `ListAfter(id > watermark LIMIT 200)` 现有消费语义。

## P1：token refresh 候选过滤与失败退避

### 问题

当前后台刷新每轮读取全部 active 账号，再在 Go 里筛选。随着账号池扩张，这个路径会浪费 DB、CPU 和外部请求：

- 非 OAuth 账号也被先取出。
- 没有 refresh_token 的账号也被先取出。
- retry exhausted 冷却中的账号仍会被先取出。
- refresh 失败后仍可能触发 privacy 外部检查。
- 无状态变化时仍可能触发 scheduler outbox。

### 建议

- 增加 `ListOAuthRefreshCandidates(ctx)`，SQL 层只返回真正可能需要后台刷新的账号：
  - `status = active`
  - `type = oauth`
  - platform in Anthropic / OpenAI / Gemini / Antigravity
  - credentials 中存在非空 refresh_token
  - 不处于 `token refresh retry exhausted:` 临时不可调度窗口
- 保留各平台 `NeedsRefresh` 的过期时间解析，避免在 SQL 里复制复杂格式兼容。
- refresh 失败路径不做 privacy 外部调用，只在刷新成功后做。
- `SetTempUnschedulable` no-op 不写 outbox。

### 可借鉴 upstream PR

- #3272：方向基本吻合，适合作为 P1 候选实现参考。

### 成功标准

- token refresh 每轮候选量显著小于 active 账号总量。
- retry exhausted 账号在冷却期内不重复刷新。
- 刷新失败时不会额外触发 privacy 外部请求。
- token_refresh 日志包含候选数、needs_refresh、refreshed、skipped、failed。

## P1：用户等待队列热路径优化

### 问题

当前 handler 外层先调用 `IncrementWaitCount`，然后 `AcquireUserSlotWithWait` 再尝试拿用户并发槽位。
即使用户槽位立即成功，也已经产生 Redis wait queue 计数操作。

对高并发代理站来说，"大部分请求不需要等待"才是常态。这部分多余 Redis Lua 调用会直接影响吞吐。

### 建议

- 把 wait queue 计数移动进 `AcquireUserSlotWithWait`：
  - 先 `TryAcquireUserSlot`。
  - 立即成功则不碰 wait queue。
  - 未拿到槽位时再 `IncrementWaitCount`。
  - 等待成功、超时、取消都确保 `DecrementWaitCount` 一次。
- 删除各 handler 外层重复的 wait count 逻辑。
- 只改用户级等待队列，不改账号级槽位和 billing cache。

### 可借鉴 upstream PR

- #3273：改动边界清晰，适合作为 P1 性能优化候选。

### 成功标准

- 用户槽位立即成功的请求不调用 wait queue Redis 命令。
- 等待路径不泄漏 wait count。
- 高 QPS 下 Redis 命令量下降，业务成功率不变。

## P1：请求 metadata 归因与跨系统 trace

### 问题

当前有 `X-Client-Request-ID` 和 request_id，但缺少业务侧可查询 metadata。一个代理商通常会在一个 API key 下服务多个终端用户、项目、功能或订单。
如果 sub2api 不保存这些标签，后续只能让代理商自行对账，平台无法提供更细的成本归因和异常排查。

同时，kiro.rs 下一步也需要 `request_trace_id`。sub2api 应该成为 trace 的入口和账单归因源。

### 建议

- 增加 bounded `X-Usage-Metadata` 或等价机制：
  - 只接受 JSON object。
  - 限制大小，例如 2KB。
  - 限制 key 数，例如 16。
  - 非法时静默忽略，不影响 LLM 请求。
- 保存到 `usage_logs.metadata jsonb`。
- 保留 `client_request_id` 做请求链路 trace，metadata 做业务归因，不混用。
- 后续给 kiro.rs 透传 `X-Client-Request-ID` 或 `X-Sub2API-Trace-ID`，让 usage row 和 kiro.rs 日志可直接关联。

### 可借鉴 upstream PR

- #3168：caller-supplied request metadata。

### 成功标准

- 任意 usage row 可带业务标签，例如 `tenant_id`、`end_user_id`、`feature`、`order_id`。
- metadata 不含敏感原文时可被后台检索/导出。
- 错误 metadata 不会导致请求失败。
- 同一请求能从 sub2api usage row 关联到 kiro.rs trace。

## P1：账号健康监控与运营视图

### 问题

账号池是 sub2api 生意的资产，但当前很多状态仍是请求路径上被动发现：

- refresh 失败。
- quota 耗尽。
- 临时不可调度。
- 模型不支持。
- 代理失效。
- 上游 401/403/429/5xx。

Kiro 这次 "7/8/9 月度额度耗尽，只剩 10 扛流量" 就说明：账号池状态应该在管理界面一眼可见，而不是靠 SSH grep。

### 建议

- 先做后端最小健康状态，不急着大 UI：
  - healthy / limited / error / paused / untested。
  - last_health_status、last_health_checked_at、last_health_error、last_health_latency_ms。
  - 按 platform、group 聚合健康率。
  - 管理端按健康状态筛选账号。
- 手动测试连接和定时检测都写健康快照。
- 对 Kiro / Claude 这类账号补 quota / disabled reason / monthly exhaustion 摘要字段。

### 可借鉴 upstream PR

- #3281：账号健康监控功能完整，但改动很大，包含迁移、Ent、后端、前端、批量定时检测。建议拆后端状态快照最小子集，先不整包合。
- #3278：账号列表显示 account id，属于低风险可用性小优化，可顺手评估。

### 成功标准

- 管理员不用查日志即可知道哪些账号可调度、哪些账号受限、原因是什么。
- 账号即将耗尽或已耗尽能提前看到。
- 健康状态不参与复杂调度，只做状态呈现和人工决策入口。

## P1：非重试错误分类，减少无效 failover

### 问题

并不是所有上游错误都应该 failover。确定性错误如果重试多个账号，只会浪费账号额度和延迟：

- context window exceeded。
- invalid request。
- 某些 schema / tool 参数错误。
- 内容策略硬拒绝。

当前已经合入了 Anthropic window cooldown 保留，但 OpenAI/Responses 长上下文场景仍值得单独梳理。

### 建议

- 给主要入口维护 narrow non-failover classifier。
- 分类只处理确定性错误，不把 transient capacity / processing 错误误判为不可重试。
- 非重试错误要保留上游原始可读 message，方便客户端修正请求。

### 可借鉴 upstream PR

- #3237：OpenAI context-window overflow 不 failover。

### 成功标准

- 超上下文请求不会打穿多个账号。
- 可重试的 429/5xx 仍保持现有 failover / cooldown 行为。
- usage 不记录成成功请求。

## P1：JSON 深度与压缩体安全防护

### 问题

sub2api 是公网网关，所有入口都会解析请求 JSON。过深 JSON 或压缩后放大的 payload 可能在进入 gjson / JSON 解析前消耗 CPU/内存。

这不是当前已观测业务 bug，但属于低成本高收益的入口安全兜底。

### 建议

- 在 body 解压后、gjson.ValidBytes / gjson.GetBytes 前做 JSON container depth preflight。
- 只计算对象/数组嵌套深度，不替代完整 JSON validation。
- 深度超过阈值，例如 512，直接 400。
- WebSocket / Responses 首包和后续 payload 也要覆盖。

### 可借鉴 upstream PR

- #3183：guard gjson parsing depth。

### 成功标准

- 合法浅层 JSON 不受影响。
- quoted brackets 不误判。
- gzip/zstd/deflate 解压后过深 JSON 被拒绝。
- 错误返回明确且不会触发上游请求。

## P2：usage 聚合与报表性能

### 问题

usage_logs 是增长最快的核心表。当前已有预聚合和部分索引，但仍有明确 TODO：`GetAllGroupUsageSummary`
会扫描全表。

只要业务成功，这个问题必然出现。

### 建议

- 给 group usage summary 增加 30s cache 或预聚合表。
- 对管理端高频报表明确哪些走实时、哪些走近实时。
- 慢查询日志按 query family 聚合，不只看单条 SQL。
- usage cleanup / archive 策略和账单审计保留期分离。

### 成功标准

- usage_logs 超过百万/千万行后，管理首页和分组列表仍稳定。
- 报表查询不会影响请求热路径。
- 成本口径和预聚合口径有回归测试。

## P2：OpenAI / image / Responses 兼容修补

### 问题

OpenAI 兼容层持续变化，尤其 Responses、图片、WS、Codex 指令。它们重要，但不是当前 Kiro 主线的最大风险。

### 候选

- #3260：normalize responses passthrough streams。
- #3234 / #3235 / #3215：图像计费准确性。
- #3271 / #3269：GPT-5.5 codex instructions。
- #3251：Chat Completions 转 Responses 时补 `strict=false`。
- #3247 / #3163：reasoning / thinking 协议处理。

### 建议

按真实客户入口分批合，不做"看到 PR 就合"：

- 如果近期卖 OpenAI/Codex 流量：优先 context-window、Responses stream、Codex instructions。
- 如果卖图片流量：优先 image billing 三件套。
- 如果当前主要还是 Claude/Kiro：这些放 P2，不抢 P0/P1 资源。

## P3：插件化、大 UI、新支付/通知能力暂缓

upstream 当前有插件化改造、邮件 provider、渠道监控 jitter、更多新模型/协议等 PR。这些不一定没价值，但不建议现在进入主线：

- 会扩大回归面。
- 对当前盈利核心没有直接改善。
- 容易掩盖账单/调度/刷新这些基础问题。

建议等 P0/P1 稳定后，再按产品路线选择。

## 建议实施顺序

1. P0 账单正确性回归与异常监控。
2. P0 scheduler_outbox 清理与写压力治理。
3. P1 token refresh 候选过滤与失败退避。
4. P1 用户等待队列热路径优化。
5. P1 request metadata + sub2api/kiro.rs trace 串联。
6. P1 账号健康监控后端最小状态。
7. P1 非重试错误分类和 JSON 深度安全防护。
8. P2 usage 聚合性能和 OpenAI/image/Responses 兼容修补。

## 验收口径

这次分析任务本身的验收标准：

- 只读分析，不修改生产逻辑。
- 明确 P0/P1/P2/P3 优先级和取舍。
- 每个建议都有成功标准。
- 区分本地代码证据、upstream PR 参考和未验证生产状态。
- 不读取或输出生产 `.env`、数据库连接串、账号凭据或 API key。
