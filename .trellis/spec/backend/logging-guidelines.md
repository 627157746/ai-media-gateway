# Logging Guidelines

> `log/slog` only — no other logging framework.

---

## Request log line

`internal/server/middleware.go` `requestLog` emits one `slog.Info("request", …)` per request after the handler returns, with fields `request_id`, `method`, `path`, `status`, `latency_ms`. Status comes from `statusWriter` (which also has `Flush` and `Unwrap()` so `http.NewResponseController` works for streaming — keep those when touching the writer).

**Never log**: `Authorization` headers, API keys, request bodies, or media content to slog/stdout. Status codes and upstream *messages* are fine. (`request_logs.params` is the documented exception for request params — see below: prompts are truncated and media bytes are never stored.)

## `request_logs` table rows

- Only model-generation calls get a `request_logs` row: `POST /v1/images/generations`, `POST /v1/images/edits`, `POST /v1/videos`, `POST /v1/audio/speech` (`loggedEndpoint` in middleware). Everything else (model list, task polls, content downloads) still gets the slog line but no row.
- Handlers fill `ModelID`, `ProviderID`, `AccountID`, `Attempts` through the ctx entry — the shared helper is `logFill` in `internal/openai/openai.go`; get the entry with `httpx.Log(ctx)` (nil-safe outside `/v1/*`).
- Error responses go through `httpx.Fail(w, r, err)`, which sets `entry.Error` before writing — always prefer `Fail` over bare `WriteError` in handlers so failures appear in `request_logs`.
- `params` (migration 004): `logParams(r, imageParams|videoParams|speechParams(req))` stores a normalized JSON summary of the parsed media request on generation endpoints — image: prompt/n/size/quality/background/resolution/response_format/edit/images/mask/extra; video: prompt/mode/seconds/size/resolution/ratio/generate_audio/watermark/first_frame/last_frame/references/videos/audios/extra; speech: input/voice/format/instructions/speed/extra. Empty/zero values are omitted; prompt/input/instructions clip at 2000 runes; `media.Input` becomes `{"url"}` / `{"name","mime","bytes"}` — **bytes and base64 are never stored**; `extra` strings >500 runes become `<已省略 N 字符>`. The 请求日志 UI renders it via the 详情 sheet.

## Levels

- `Info` — request lines, lifecycle (`listening`, `pruned request_logs`), in `middleware.go`/`main.go`.
- `Warn` — degraded-but-tolerated: video poll failures that keep task state (`videos.go` `pollTask`: `slog.Warn("video poll failed, keeping state", …)`), prune failures.
- `Error` — panics in the recoverer (`middleware.go` logs `request_id` + `err` + `path`), fatal startup errors in `main.go`.

Tests don't assert on slog output; keep slog out of pure libraries (`media`, `provider` adapters return errors instead of logging).
