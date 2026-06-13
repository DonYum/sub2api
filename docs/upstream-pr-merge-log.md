# Upstream PR Merge Log

## 2026-06-14: Wei-Shaw/sub2api #3252 + #3195 + #3250 + #3154

- Maintained repo: `DonYum/sub2api`
- Upstream repo: `Wei-Shaw/sub2api`
- Base: `origin/main` at `a505e46d`
- Branch: `feature/upstream-pr-3252-3195-3250-3154`

| Upstream PR | Upstream commit(s) | Local commit(s) | Why merge | Conflict / resolution |
| --- | --- | --- | --- | --- |
| #3252 | `c1c28ac7` | `514443a7` | Decode zstd upstream responses so usage parsing sees JSON instead of compressed bytes. This prevents silent zero-token usage on zstd responses. | Clean cherry-pick. `github.com/klauspost/compress` was already present in `backend/go.mod`. |
| #3195 | `fa8f1749`, `727ac3f6` | `b45b15d1`, `41241c69` | Treat `invalid_refresh_token` and `app_session_terminated` as non-retryable refresh errors so dead OAuth sessions are not hammered repeatedly. | Clean cherry-pick. |
| #3250 | `f6e0ebc6` | `d09c888a` | Preserve official Anthropic window cooldowns instead of shortening them with local temporary-unschedulable rules. | Clean cherry-pick. No conflict with the maintained sticky fallback fix because this touches rate-limit cooldown preservation, not sticky binding. |
| #3154 | `a67b10f4` | `02d99963` | Use OpenAI Responses `input` as the sticky hash content fallback when `messages` is absent, reducing unrelated session collisions for Responses traffic. | Clean cherry-pick. Reviewed against the maintained low-priority fallback sticky binding fix; no overlapping binding-path changes. |
| #3094 | upstream merged | already present: `7b394ed1` | Clear stale sticky bindings when accounts leave a group. | Already ancestor of current branch; skipped to avoid duplicate merge. |
| #2997 | upstream merged | already present: `96b4bf62` | Bind OpenAI Responses `response.id` to account for continuation routing. | Already ancestor of current branch; skipped to avoid duplicate merge. |

### Verification

- `gofmt`: applied to changed Go files.
- `git diff --check`: passed.
- `cd backend && go test ./internal/repository ./internal/service -run 'Test.*(Decompress|Refresh|Invalid|SessionHash|Responses|Window|RateLimit|Anthropic)' -count=1`: passed.
- `cd backend && go test -tags unit ./internal/service -run 'TestGatewaySticky|TestOpenAIGatewayService_GenerateSessionHash|TestGenerateSessionHash' -count=1`: passed.
- `cd backend && go test ./internal/repository -run 'Test.*Decompress' -count=1 && go test ./internal/service -run 'Test.*(Refresh|Invalid|SessionHash|Responses|Window|RateLimit|Anthropic)' -count=1`: passed.
- Broader `cd backend && go test ./internal/repository ./internal/service ./internal/handler ./internal/handler/admin -count=1`: `internal/service`, `internal/handler`, and `internal/handler/admin` passed; `internal/repository` failed on pre-existing `usage_log_repo_request_type_test.go` sqlmock/scan argument-count mismatches. The same `internal/repository` failure reproduces on `origin/main` at `a505e46d`, so it is not introduced by this upstream merge.

### Notes For Future Upstream PR Merges

- Check whether merged upstream PRs are already ancestors before cherry-picking merged PRs; this branch already contained #3094 and #2997.
- For sticky changes, diff both hash generation and account-binding paths. Hash fallback changes can be safe while binding changes may conflict with maintained low-priority fallback handling.
- Keep billing, DB schema, and migration changes out of small operational-fix merges unless the target PR explicitly requires them and they are reviewed separately.
