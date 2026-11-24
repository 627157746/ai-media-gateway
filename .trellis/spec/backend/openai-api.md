# OpenAI API Contract

> The external `/v1/*` surface (`internal/openai/`) — this is what clients (Infinite Canvas) code against. `Infinite Canvas.md` at repo root is the client contract source; keep behavior aligned with it.

---

## Auth & CORS

- `/v1/*` requires `Authorization: Bearer sk-mg-…` (SHA-256-checked gateway key, `store.AuthenticateKey`) **or** `Bearer <ADMIN_TOKEN>` (constant-time compare — the built-in 试用 page uses this; admin-token callers get no APIKey in ctx). `/admin/*` requires `Bearer <ADMIN_TOKEN>` (constant-time compare). `OPTIONS` preflight passes without auth → 204.
- CORS (`server/middleware.go`): `Allow-Origin` per `CORS_ORIGINS` (`*` default, else echo matching origin + `Vary: Origin`); echoes `Access-Control-Request-Headers` back in `Allow-Headers` (else default `Authorization, Content-Type, X-Request-Id, Range`); `Max-Age: 600`; exposes `Content-Length, Content-Type, X-Request-Id, Content-Range, Accept-Ranges`.
- All errors are the OpenAI shape `{"error":{"message","type","code"}}` — see `error-handling.md`.
- **Canvas note**: set channel type `OpenAI` and `baseUrl` = `http://<host>:8080` with **no `/v1`** — the Canvas app appends `/v1` itself.

## Endpoints (`internal/openai/`)

| Method/Path | File | Notes |
|---|---|---|
| `GET /healthz` | server | no auth |
| `GET /v1/models` | `images.go listModels` | `{"object":"list","data":[{"id","object":"model","owned_by":<provider id>,"kind"}]}` of enabled models on enabled providers |
| `POST /v1/images/generations` | `images.go` | JSON only |
| `POST /v1/images/edits` | `images.go` | JSON or multipart |
| `POST /v1/videos` | `videos.go createVideo` | JSON or multipart → task object |
| `GET /v1/videos/{id}` | `videos.go getVideo/pollTask` | poll (≥1s between upstream polls via `last_polled_at`) |
| `GET /v1/videos/{id}/content` | `videos.go videoContent` | Range proxy |
| `GET /v1/videos?limit=` | `videos.go listVideos` | default 20, max 100 |
| `POST /v1/audio/speech` | `speech.go` | streamed audio |

Request body size is capped by `MAX_BODY_MB` (100) middleware — multipart uploads go through it.

## Images

- JSON fields: `model` (required), `prompt` (required), `n` (default 1, max 10), `size`, `quality`, `background`, `resolution`, `response_format` (`url` default | `b64_json`), `extra` (vendor passthrough map).
- Edits accept either JSON (`images`: array of data-URL / http-URL strings or `{"image_url":…}` objects, `mask` same) or multipart (fields above + `extra` as a JSON string; files `image`, `image[]`, `mask`).
- Response: `{"created":<unix s>,"data":[{"url"|"b64_json"}…]}`. With `response_format=b64_json`, URL-only results are fetched (`provider.FetchURL` — vendor URLs only) and base64'd; B64-native assets always come back as `b64_json`.

## Videos

- Accepted fields (both JSON and multipart): `model`, `prompt`, `seconds`, `size`, `resolution` **or** `resolution_name`, `ratio` **or** `aspect_ratio`, `generate_audio`, `watermark` (bool strings `"true"`/`"false"` allowed), `mode`, `extra` (JSON object or JSON string).
- File/media inputs: `first_frame`, `last_frame`, `image`/`image[]`, `input_reference` (OpenAI field — treated as first frame), `video`/`video[]`, `audio`/`audio[]`; JSON variants `images`, `first_frame`, `last_frame`, `input_reference`, `videos`, `audios` as data-URL or http(s) URL strings. http(s) URLs pass through to the vendor — never fetched (SSRF).
- **Mode normalization** (`normalizeMode`, pinned by `TestVideoModeNormalization`): `mode=="frames"` without first/last → `images[0]`→first, `images[1]`→last; `mode==""` → first/last given ⇒ `frames`, >2 images ⇒ `reference`, 1–2 images ⇒ `frames`; `mode=="reference"` → all images (+ first/last if given) become `References`.
- On `SubmitVideo` success a `video_tasks` row (`id` = `"video_"+24hex`) is bound to the account the pool used → 200 with the task object.
- **Task object**: `{"id","object":"video","model","status","progress","created_at"(unix s),"completed_at"(unix s|null),"video_url"(only when completed),"error":null|{"message","code"}}`.
- `GET /{id}` 404s unknown tasks; transient-class poll failures keep state (`slog.Warn`), others mark `failed`; completed sets `video_url` + `completed_at`.
- `GET /{id}/content`: completed only else 409 `video_not_ready`; forwards the client's `Range`, copies upstream status (200/206) + `Content-Type/Content-Length/Content-Range/Accept-Ranges`, streams the body.

## Speech

`POST /v1/audio/speech` JSON `{model, input (required), voice, response_format, speed, instructions, extra}`. `Do(Speak)` runs inside the pool; the returned `io.ReadCloser` is streamed with a flush per ~32KB chunk via `http.NewResponseController` (which needs `statusWriter.Unwrap` — don't remove it). Upstream `Content-Type` is passed through.

## Model resolution for clients

Client sends `model=<id>` → `accounts.Resolve` (see `account-pool.md`): registered alias first, else `<provider>/<upstream-endpoint>` passthrough when the provider has `passthrough` enabled. Handler type-asserts the adapter to the needed interface (`ImageGenerator`/`VideoGenerator`/`SpeechGenerator`); unsupported → 400 `wrong_kind`.

## Admin API surface (`internal/admin/admin.go`)

Providers `GET/POST /admin/providers`, `GET/PATCH/DELETE /admin/providers/{id}` (409 if dependents); accounts under `/admin/providers/{id}/accounts` + `/admin/accounts/{id}` CRUD/reset; models `/admin/models` CRUD; gateway keys `GET/POST /admin/keys`, `GET /admin/keys/{id}/secret` (returns plaintext from `key_enc`; 400 for pre-migration hash-only keys), `DELETE /admin/keys/{id}`; `/admin/tasks`, `/admin/logs?account=&since=<unix-ms|RFC3339>&limit=`, `/admin/status` (providers with nested accounts+state — the UI's single data source), `/admin/provider-types` (`{"data":[sorted types]}`); `GET/PATCH /admin/settings` (currently `{"proxy_url"}`); `POST /admin/proxy/test` `{"proxy_url","target?"}` → `{"ok","status","latency_ms"}`/`{"ok":false,"error"}` with proxy password masked; `/admin/proxies` CRUD (proxy pool — entries carry `url_masked`+`used_by`; DELETE → 409 while referenced). `proxy_url` on providers/accounts + settings accepts `""` (inherit), `direct`, `proxy:<id>`, or http/https/socks5/socks5h URL — precedence account > provider > global (`netproxy.Effective` + `accounts.ResolveProxy`). PATCH = partial update with pointer fields. `PATCH /admin/models/{id}` accepts `id` to rename the model — the rename also rewrites `video_tasks.model_id` (stored by value, no FK) in the same transaction. `POST /admin/accounts/{id}/test` is intentionally not implemented yet.

The embedded admin UI (`internal/web`, `GET /` + `/assets/*`) is served **without auth** — all its data calls carry the admin token, so keep static routes auth-free and never expose data on the static paths. `/admin/logs since` accepts unix-ms or RFC3339.
