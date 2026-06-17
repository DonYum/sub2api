# Kiro 缓存命中与计费调查（sub2api 侧）

> 本文档记录 2026-06 针对"Claude Code 经 sub2api → kiro.rs → Kiro 链路缓存命中率差、计费偏高"问题的完整调查与 sub2api 侧的改动。配套文档见 kiro.rs 仓库 `docs/kiro-cache-investigation.md`。

## 1. 背景问题

用户现象：用 sub2api 代理的 Kiro 账号池，十几个 API key 中只有个别 key 缓存命中正常，其余很差；
且怀疑缓存命中了但计费按"未命中"满价收，多收客户钱。

最终定位为**多个独立问题叠加**，跨 sub2api 和 kiro.rs 两个项目。本文记 sub2api 侧。

## 2. 问题一：sticky 绑定绕过优先级，永久锁定低优先级兜底账号

### 现象
"只有个别 key 缓存好、其他差"——根因是不同会话被锁在不同上游账号上，体验不一。

### 根因
sub2api 选账号时**先查 sticky 绑定，命中即返回该账号、完全跳过优先级**；只有无绑定才按优先级选。
而 sticky 绑定一旦建立会**滑动刷新 TTL**。于是：某次所有高优先级账号恰好临时不可用
（限流/RPM 满/配额窗口/并发槽满/健康快照过期），优先级循环 fallback 到最低优先级兜底账号
（如 `cfjwlpro`），**这个选择立刻被写成 sticky 绑定并靠滑动 TTL 永久续命**——会话被永久锁死在
兜底账号上，高优先级账号恢复后也不回切。

因为兜底账号恰好是缓存好的外部账号，黏上的用户体感好（恰好是某 Claude Code 版本用户，造成
"版本相关"的幸存者偏差）；黏到 Kiro 账号池的用户缓存差。

### 修复
`gateway_service.go`：选中最低优先级层（fallback）账号时**不写 sticky 绑定**。账号仍服务当次请求，
但不锁定，下次请求重走优先级、高优先级恢复后自动回切。
- 用 group 内**相对优先级**判定（最高优先级层 = priority 最小值那档）。
- 退化情况 fail-open：单档优先级 / 取不到 group priority / groupID 为 nil 时照常绑定，不误伤正常 sticky。
- 上线后清理 Redis 中 value 指向最低优先级账号的 `sticky_session:*` key（仅定向清理，不全清）。
- 详见 PR #1（已合并 main）。

## 3. 问题二：缓存命中却按满价 token 计费，多收客户

### 根因
sub2api 计费是纯 token 口径：`total_cost = input×价 + output×价 + cache_creation×1.25 + cache_read×0.1`，
`actual_cost = total_cost × rate_multiplier`。其中 cache_read 单价仅为 input 的 1/10。

但 Kiro 对**企业 SSO 账号**只返回 `meteringEvent` 的 credits，**不返回**标准 Anthropic cache token
（`cache_creation_tokens` / `cache_read_tokens` 恒为 0，详见 kiro.rs 侧文档）。缓存命中时
`input_tokens` 仍是满量，于是 sub2api 按满价 input 计费 → **客户被多收、毛利虚高**。

### 修复（`gateway_service.go` `applyKiroCreditsCostOverride`）
当 `result.Usage.UpstreamKiroCredits != nil` 时，改用 Kiro 真实 credits 作为计费基准：
- `total_cost = upstream_kiro_credits`
- `actual_cost = total_cost × account_rate_multiplier × rate_multiplier`
- `account_stats_cost` 同步用 credits 基准
- **不伪造** `cache_creation_tokens` / `cache_read_tokens`（保持上游事实）
- 负 credits 归零；非 Kiro 请求不进入 override

经济正确性：用户的 power plan 10000 credits = $10000，即 **1 credit = $1**；且 credits 数值
≈ Anthropic 等价美元成本（受控实验 credits 0.1726 ≈ 10638 tok × $15/MTok ≈ $0.16）。
账号倍率用于把具体 Kiro 账号成本折算进客户实际扣费；分组/用户倍率继续用于客户定价或折扣。

## 4. 问题三：缓存省钱效果不可见

`cache_creation_tokens` / `cache_read_tokens` 在 Kiro 链路恒为 0（账号体系所致），用户盯着它会误以为
"没缓存"。但直接把 credits 塞进这两个字段是错的（维度对不上：credits 是一个成本数，这俩是两个 token 计数；
且会再次污染计费与标准语义）。

### 修复（展示层派生指标，`dto/mappers.go` `EnrichKiroCostEstimates`）
在 usage API 响应层追加三个**纯展示、不持久化、不参与计费**的派生字段：
- `kiro_list_price_cost_estimate`：按 input/output token × Anthropic 标准价算的满价成本估算
- `kiro_savings_cost_estimate`：原价估算 − credits
- `kiro_discount_rate_estimate`：1 − credits/原价估算
原价估算复用 `BillingService.CalculateCostUnified` + `ModelPricingResolver`，**与计费用同一定价源**
（LiteLLM 动态价 / fallback / 渠道定价），保证前端展示与真实计费不漂移。除零有保护
（`cost.TotalCost <= 0` 直接返回）。前端用户/管理员 usage 列表、tooltip、CSV/Excel 导出均展示。

## 5. 验收口径（重要）

- **不要用"复用 session 的原始 credits 是否都下降"判断缓存是否生效。** 真实多轮对话每轮新增内容是
  无缓存、需全价的，每轮 credits 会停在"缓存前缀(便宜)+新增内容(全价)"水平，不会降到接近 0。
- **正确指标是 `kiro_discount_rate_estimate`**（credits vs 全价 list price），它扣掉了"新增内容"变量。

## 6. 关联的 kiro.rs 侧改动

- `upstream_kiro_*` 字段由 kiro.rs 在响应 usage 里透传（kiro.rs `feature/kiro-metering-usage`）。
- kiro.rs 还做了会话级账号粘性（`feature/kiro-session-sticky`），解决 balanced 模式下同会话被打散到
  不同 Kiro 账号导致的缓存丢失——这是 sub2api sticky 之下的第二层粘性。

## 7. 标准 cache token 路线为何关闭

Kiro 标准 `metadataEvent.tokenUsage`（含 cacheReadInputTokens）只在 Amazon Q CLI runtime + 社交登录
账号路径返回；用户账号是 AWS IAM Identity Center 企业 SSO，已验证在所有 endpoint/身份组合下仍只返回
meteringEvent credits。故标准 cache token 不可得，credits 计费 + 折扣率估算是该账号体系下的正确方案。
换其他 Kiro 代理软件（kiro-go/KiroProxy）不能解决（瓶颈是账号类型非软件）。

## 8. 相关 PR

- PR #1（已合并）：sticky 绑定不锁定低优先级 fallback 账号。
- PR #2：记录 Kiro metering credits + 按 credits 计费 + 派生折扣率展示。
