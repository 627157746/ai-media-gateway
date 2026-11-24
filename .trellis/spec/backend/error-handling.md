# Error Handling

> Sentinel errors, `media.UpstreamError` classification, and the OpenAI-shaped HTTP mapping.

---

## Sentinels

`internal/media/media.go`: `ErrBadRequest`, `ErrModelNotFound`, `ErrWrongKind` — always wrapped with `%w` (`fmt.Errorf("%w: ...", media.ErrBadRequest)`) so `errors.Is` survives. `store.ErrNotFound` / `store.ErrConflict` work the same way (see `database-guidelines.md`).

## `media.UpstreamError`

Adapters never return raw HTTP/transport errors; they return `*media.UpstreamError{Class, Status, Code, Message, RetryAfter, Final}`.

- `ErrorClass`: `ClassAuth`, `ClassQuota`, `ClassRateLimit`, `ClassUpstream`, `ClassInvalid`, `ClassContent`. `ErrorClass.Failover()` is true only for Auth/Quota/RateLimit/Upstream — the pool tries the next account for those; Invalid/Content stop immediately.
- **`Final`** = "never fail over even if the class allows it". Set when a retry could duplicate a paid job: `provider.TransportError(err, submit)` sets `Final = submit && !isDialError` (dial/DNS failures mean nothing reached the vendor → safe to retry); fal's `finalErr` marks **every** post-submit error Final with the `request_id` in the message (`internal/provider/fal/fal.go`). Keep these semantics — see `TestSubmitTransportFinal`, `TestPostSubmitErrorFinal` in `fal_test.go`.
- ctx cancellation passes through unwrapped (`errors.Is(err, context.Canceled)` stays true) so the pool can distinguish client cancel from upstream failure.
- `Error()` renders `"<class>: <message>"` plus ` (HTTP <status>)` when `Status > 0` and ` [<code>]` when `Code != ""` — e.g. `quota: insufficient balance (HTTP 403)`. This string lands in `request_logs.error` and admin `last_error`, so never start `Message` with "upstream" (the class already says it).

## Vendor status → class tables (as implemented)

| Status | fal (`fal.go statusError`) | ElevenLabs | Fish Audio |
|---|---|---|---|
| 401 | `ClassAuth` | `ClassQuota` if `detail.status=="quota_exceeded"` else `ClassAuth` | `ClassAuth` |
| 402 | `ClassQuota` | `ClassQuota` | `ClassQuota` |
| 403 | `ClassQuota` if body (lowercased) contains `balance`/`exhausted`/`locked`/`credit`/`billing`, else `ClassAuth` | — | `ClassAuth` |
| 429 | `ClassRateLimit` + `Retry-After` (`provider.ParseRetryAfter`) | same | same |
| 400/404/422 | `ClassInvalid` (fal message from `detail` string or `{msg,loc}` array) | 400/422 → `ClassInvalid` | 400/422 → `ClassInvalid` |
| 5xx | `ClassUpstream` | `ClassUpstream` | `ClassUpstream` |
| transport | `TransportError(err, true)` on submit, `(err,false)` otherwise | `TransportError(err, true)` | `TransportError(err, true)` |

Use the shared helpers in `internal/provider/httputil.go` (`ReadErrorBody` ≤64KB, `ParseRetryAfter`, `TransportError`, `FetchURL`) — don't re-implement per adapter.

## Pool-level errors

- `accounts.NoAccountError` (all accounts disabled/cooling) carries `SoonestRetry`; `httpx.WriteError` → 503 `no_account_available` with the RFC3339 retry time in the message.
- Pool stops without cooldown on ctx errors and non-`UpstreamError` errors; failover-able non-Final errors cool the account and try the next one (see `account-pool.md`).

## HTTP mapping — `httpx.WriteError` / `httpx.Fail`

All `/v1` and `/admin` errors are emitted via `httpx` in the OpenAI shape `{"error":{"message","type","code"}}`:

Sentinel/pool rows list the `code`; upstream rows list the `type` (their `code` is the vendor's `UpstreamError.Code`).

| Error | HTTP | code / type |
|---|---|---|
| `ErrBadRequest` | 400 | `bad_request` |
| `ErrModelNotFound` | 404 | `model_not_found` |
| `ErrWrongKind` | 400 | `wrong_kind` |
| `store.ErrNotFound` | 404 | `not_found` |
| `store.ErrConflict` | 409 | `conflict` |
| `NoAccountError` | 503 | `no_account_available` |
| Upstream `ClassAuth` | 502 | `upstream_auth` |
| Upstream `ClassQuota` | 402 | `insufficient_quota` |
| Upstream `ClassRateLimit` | 429 (+`Retry-After`) | `rate_limit_exceeded` |
| Upstream `ClassInvalid` | 400 | `invalid_request_error` |
| Upstream `ClassContent` | 400 | `content_policy_violation` |
| Upstream `ClassUpstream` | 502 | `upstream_error` |
| anything else | 500 | `internal_error` |

Handlers must use **`httpx.Fail(w, r, err)`**, not `WriteError` directly — `Fail` also records the error on the request_log entry in ctx (`httpx.Log`).

## Video poll error policy (`internal/openai/videos.go` `pollTask`)

- Transient classes (Upstream/RateLimit/Auth/Quota) on `PollVideo` → keep task state, update `last_polled_at`, `slog.Warn`, return current state.
- `Invalid`/`Content`/other → mark task `failed` with the message.
- Deleted bound account → mark failed (`"account <id> was deleted"`). Polls never fail over — `CallFor` pins the submitting account.

**Pinned by tests**: `openai_test.go` (`TestVideoPollTransientKeepsState`, `TestVideoDeletedAccountFails`, `TestVideoFailoverAndAffinity`), `fal_test.go` (`TestErrorMapping`, `TestTransientStatusErrorsRetried`, `TestTransientStatusErrorsGiveUp`, `TestPollOwnTimeout`), `elevenlabs_test.go`/`fishaudio_test.go` error-mapping cases.
