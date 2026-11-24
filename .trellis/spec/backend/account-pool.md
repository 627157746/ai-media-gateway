# Account Pool

> Multi-account selection, failover, and cooldown — `internal/accounts/pool.go`. The core scheduling logic of the gateway.

---

## Model resolution first

`accounts.Resolve(ctx, st, modelID, kind)` (`resolve.go`) runs before the pool:

1. Exact `models.id` match (enabled) → `kind` must equal or `ErrWrongKind`; `options.accounts` becomes `Resolved.AllowedAccounts`.
2. Otherwise split at the first `/` → `provider/rest` passthrough: provider must exist, be enabled, and have `options.passthrough` not `false` → synthetic `ModelConfig{ID: modelID, Kind: kind, Upstream: rest}`.
3. Otherwise `ErrModelNotFound`.

Tests: `resolve_test.go` (alias, wrong kind, passthrough on/off, not found).

## Eligibility (`Pool.eligible`)

An account is eligible iff: `enabled && (cooldown_until == nil || <= now) && (AllowedAccounts empty || id ∈ AllowedAccounts)`. The provider itself must be enabled (checked in `Resolve`/`enabledProvider`).

## Attempt order (`attemptOrder`)

- Accounts are grouped by `priority` **descending**; every account in a higher group is tried before any account in a lower group.
- Within a group: **smooth weighted round-robin** (nginx SWRR). The SWRR pick leads that group's attempt list, the rest follow in descending current-weight order (`rrGroup.pick`).
- In-memory RR state is keyed by `providerID + "|" + sorted eligible ids`, and each group resyncs when its **membership** differs (not just size — a priority swap rebuilds `g.order`; `TestPrioritySwapResyncsGroups` in `pool_test.go` pins this). State map is capped at `maxRRStates` entries and guarded by `p.mu`.
- `MAX_ATTEMPTS` env (`cfg.MaxAttempts`, `0` = unlimited) truncates the attempt list.
- 2:1 weights over 30 `Do` calls → exactly 20/10 (`pool_test.go`).

## Error → cooldown (`Pool.cool`)

On a `*media.UpstreamError` with `Class.Failover() && !Final` the account is cooled and the next account is tried:

| Class | Cooldown | Env |
|---|---|---|
| `ClassRateLimit` | `RetryAfter` if set, else `COOLDOWN_RATELIMIT` (60s) | `COOLDOWN_RATELIMIT` |
| `ClassUpstream` | `COOLDOWN_UPSTREAM` (30s) × 2^(fail_count−1), capped at `COOLDOWN_UPSTREAM_MAX` (10m) | `COOLDOWN_UPSTREAM`, `COOLDOWN_UPSTREAM_MAX` |
| `ClassQuota` | `COOLDOWN_QUOTA` (1h) | `COOLDOWN_QUOTA` |
| `ClassAuth` | `COOLDOWN_AUTH` (24h) | `COOLDOWN_AUTH` |

`Invalid`/`Content`/`Final`/non-`UpstreamError` → return immediately, no cooldown, no failover. `ctx` cancellation (`ctx.Err()`, `errors.Is(Canceled/DeadlineExceeded)`) → return immediately, no cooldown.

Bookkeeping: `RecordSuccess` (fail_count=0, success_count++, last_used_at) and `RecordFailure` (failure_count++, fail_count++, last_error/last_error_at) use `context.WithoutCancel` so they persist even if the client disconnected. Manual reset: `POST /admin/accounts/{id}/reset`.

If every attempt fails with failover-able errors, `Do` returns the **last** `UpstreamError` — a single-account provider surfaces the real vendor error, not a pool-level one. If nothing was eligible → `*NoAccountError` with `SoonestRetry` (soonest `cooldown_until` among enabled accounts) → HTTP 503.

`Result.AccountID` is set to the **last attempted account on every return path after ≥1 attempt** — success, all-failed, non-failover/`Final` error, and ctx-cancel — so handlers can record it in `request_logs` even when the request fails (`TestResultAccountIDOnFailure`, `TestFailedRequestLogsAccountID`).

## Weight

`weight` must be ≥ 1: the admin API rejects `weight < 1` with 400 on create **and** PATCH, and defaults to 1 when omitted (`TestAccountWeightValidation`). SWRR still defends with `weight <= 0` treated as 1, and migration `002_weight_min.sql` normalized legacy rows to 1.

## Task affinity (`CallFor`)

`Pool.CallFor(ctx, resolved, accountID)` builds a `provider.Call` for one specific account **ignoring** enabled/cooldown — used by video polling (`GET /v1/videos/{id}`) because the task row stores the account that submitted it. Deleted account → error → task marked failed. Polls never fail over.

## The fail-over window rule

Failover is only safe **before bytes are written to the client and before the task row exists**:

- `Do` may try multiple accounts inside the handler's `fn` (submit/whole-generation calls only).
- Once `SubmitVideo` succeeded and the `video_tasks` row is inserted, all later polls use `CallFor` — never `Do`.
- Any error an adapter returns after its submit call must be `Final` (see `error-handling.md`) so `Do` won't retry it on another account.
- `/v1/audio/speech` starts streaming after `Do` returns — an error mid-stream can only truncate the response; keep adapter failures `Final` post-submit.

Everything above is pinned by `pool_test.go` and `openai_test.go` (`TestVideoFailoverAndAffinity` — A gets 401 → B submits → polls hit only B).
