# Logging Guidelines

> `log/slog` only — no other logging framework.

---

## Request log line

`internal/server/middleware.go` `requestLog` emits one `slog.Info("request", …)` per request after the handler returns, with fields `request_id`, `method`, `path`, `status`, `latency_ms`. Status comes from `statusWriter` (which also has `Flush` and `Unwrap()` so `http.NewResponseController` works for streaming — keep those when touching the writer).

**Never log**: `Authorization` headers, API keys, request bodies, prompts, or media content. Status codes and upstream *messages* are fine.

## `request_logs` table rows

- For `/v1/*` paths, the middleware creates a `*store.RequestLog` and puts it in the request ctx via `httpx.WithLog`, then inserts it after the handler returns (endpoint, status, latency filled by middleware).
- Handlers fill `ModelID`, `ProviderID`, `AccountID`, `Attempts` through the ctx entry — the shared helper is `logFill` in `internal/openai/openai.go`; get the entry with `httpx.Log(ctx)` (nil-safe outside `/v1/*`).
- Error responses go through `httpx.Fail(w, r, err)`, which sets `entry.Error` before writing — always prefer `Fail` over bare `WriteError` in handlers so failures appear in `request_logs`.

## Levels

- `Info` — request lines, lifecycle (`listening`, `pruned request_logs`), in `middleware.go`/`main.go`.
- `Warn` — degraded-but-tolerated: video poll failures that keep task state (`videos.go` `pollTask`: `slog.Warn("video poll failed, keeping state", …)`), prune failures.
- `Error` — panics in the recoverer (`middleware.go` logs `request_id` + `err` + `path`), fatal startup errors in `main.go`.

Tests don't assert on slog output; keep slog out of pure libraries (`media`, `provider` adapters return errors instead of logging).
