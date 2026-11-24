# Simplify provider/account admin UX

## Goal

User requirement: "方便而且功能要全". The 供应商 page must be usable on first sight (pick a platform, paste keys, done) while every capability the backend supports at the provider/account level stays reachable through plain controls. No raw JSON, no lost features. DB schema and routing semantics stay unchanged; the only backend addition is the R9 model-list endpoint.

## Background (confirmed by code inspection)

- Data model is already provider 1 → N accounts (`internal/store/migrations/001_init.sql`); the pool picks by priority group, then weighted round-robin, with cooldown on failure.
- 「＋ 添加账号」 sits at the bottom of each provider card right above 「＋ 添加供应商」 (`internal/web/assets/pages/providers.js` `providerCard` / `render`), so it is easy to miss.
- Provider fields: `id` ([a-z0-9-]+, immutable), `type`, `base_url` (blank = type default), `options`, `enabled`. Account fields: `name`, `api_key`, `base_url` (blank = inherit), `priority`, `weight` ≥ 1, `options`, `enabled` (`internal/admin/admin.go`).
- Option keys read by the backend at provider/account level:
  - Provider `passthrough` (bool, default true when absent; `internal/accounts/resolve.go:68-76`).
  - `voices` / `default_voice` at account → model → provider (first hit wins; `internal/provider/voice.go:6-25`). Used by `elevenlabs`, `fishaudio` and `fal` speech (`fal.go:636`), so it applies to all three types.
- Admin API: `POST /admin/providers/{id}/accounts` creates one account per call; `PATCH` replaces `options` wholesale only when the field is sent.

## Requirements

- **R1 One-step add (「＋ 添加平台」)**: the sheet shows platform choice (fal / ElevenLabs / Fish Audio, from `/admin/provider-types`) and a multi-line 「API Key（每行一个，可留空稍后添加）」. The provider is saved first, then one account per key. A collapsed 「更多设置」 holds: ID (auto-filled with the type name, or `<type>-2`, `-3`… if taken; editable), custom Base URL (placeholder = type default), 允许直通调用 (default on), 启用. If a provider of the chosen type already exists, show a hint that keys can be added to the existing card instead (adding a second one stays allowed).
- **R2 Provider edit sheet**: type (read-only display), Base URL, 允许直通调用 switch (→ `passthrough`; on when absent or true), 启用, and 音色设置 (R4) in a collapsed section. No JSON textarea.
- **R3 Add-key / edit-account sheet**: visible by default: API Key (create: multi-line batch; edit: single password input, blank = unchanged) and 备注名. Collapsed 「更多设置」: 优先级 (with hint 大者先用), 权重 (≥1, hint 同优先级内按比例轮流), 自定义 Base URL (blank = inherit), 启用, 音色设置 (R4). No JSON textarea.
- **R4 音色设置 widget** (all types, collapsed by default, shared by provider and account sheets): 默认音色 input (→ `default_voice`) and 音色别名 rows (alias → vendor voice id, add/remove; → `voices`), with a one-line explanation of the lookup order (账号 → 模型 → 平台).
- **R5 Batch semantics**: keys are trimmed; blank lines and duplicates are dropped. All share the sheet's settings. With more than one key and a non-empty 备注名, names are `<name>-1`, `<name>-2`, …. Sequential POSTs. On partial failure: report the failed count and first error, keep only the failed keys in the textarea, refresh the list. In R1, if the provider create fails, no accounts are attempted.
- **R6 Layout & guidance**: each provider card header shows type, `N 个 key 轮流使用` (or 「还没有 key」), and a prominent 「＋ 添加 key」 button next to the enabled switch / 编辑 / 删除. Account rows unchanged otherwise. The page shows one hint line: one platform can hold many API keys, rotated by priority/weight, failing keys cool down automatically. The models page hint that mentions `options.passthrough` refers to the 允许直通调用 switch instead.
- **R8 Model sheet as a form** (user follow-up: "不要option这种，给我改成表单", which covers every Options box): replace the model Options textarea and the template picker with controls for every key the backend reads (`internal/provider/fal/fal.go:33-41, 470-660`, `resolve.go` `accountAllowlist`, `voice.go`). Basic: ID, 平台, 类型, 上游端点, 启用. Collapsed sections, shown only where relevant:
  - fal + image: 编辑端点 (`upstream_edit`), 尺寸参数方式 select (`fields.size_mode`: image_size default / aspect_ratio / none).
  - fal + video: 图生视频端点 (`upstream_image`), 参考生视频端点 (`upstream_reference`), 时长格式 select (`fields.duration_format`: string default / int / {n}s).
  - fal, any kind: 参数映射 rows (logical key select per kind → vendor field name; blank = omit the field; stored in `fields`), 固定参数 rows (`defaults`; the value is parsed as a JSON literal when valid, e.g. `5`, `true`, otherwise kept as a string), 移除参数 (`drop`, comma-separated).
  - speech (any type): 音色设置 (the R4 widget).
  - any: 限定账号 (`accounts`): checkboxes of the chosen platform's accounts; none checked = all accounts.
- **R9 Fetch models from the platform** (user follow-up: "没法获取模型吗"; scope: models only, not voices). On the models page, 「从平台获取」 opens a searchable list of the platform's upstream models. The user ticks entries and imports them in one click: ID, 类型 and 上游端点 are auto-filled, the ID stays editable, and entries already registered are marked. Sources (verified 2026-11-24):
  - fal: `GET https://api.fal.ai/v1/models` (q, category, status, limit, cursor, repeated endpoint_id; auth optional, `Key` header raises rate limits). Live categories include text-to-image, image-to-image, text-to-video, image-to-video, video-to-video, text-to-speech. Importing a text-to-video endpoint auto-links sibling `…/image-to-video` / `…/reference-to-video` endpoints into `upstream_image` / `upstream_reference`, and a text-to-image endpoint auto-links an edit endpoint into `upstream_edit`, when those siblings exist.
  - ElevenLabs: `GET {base}/v1/models` (`xi-api-key`), keeping `can_do_text_to_speech` models.
  - Fish Audio: no TTS-model list API exists (the `model` header enum is s1, s2-pro, s2.1-pro, s2.1-pro-free, drama-3, s3-pro; s1 is deprecated and retires 2026-12-31), so a built-in list is used.
  - Calls go through a new backend admin endpoint using a stored account key. The key never reaches the browser.
- **R7 Option preservation**: structured controls merge into stored `options`. Unknown keys are kept. Empty `default_voice` / empty `voices` delete their keys. `passthrough` is always written explicitly from the provider sheets.

## Acceptance Criteria

- [ ] From an empty DB: 「＋ 添加平台」 → pick fal → paste 2 keys → 保存 creates provider `fal` with 2 accounts, without touching 更多设置. Repeating with fal creates `fal-2`, with the existing-platform hint shown.
- [ ] 「＋ 添加 key」 with 3 lines creates 3 accounts. 备注名 `main` gives `main-1..3`, and a blank name gives empty names.
- [ ] Turning off 允许直通调用 and saving stores `passthrough:false`, and `fal/<endpoint>` stops resolving. Turning it back on restores it.
- [ ] Provider/account voice settings save and round-trip on re-open for every type. Stored extra keys (e.g. `{"foo":1}`) survive a save.
- [ ] Priority, weight, Base URL and 启用 remain settable at account level; Base URL and 启用 at provider level. No provider/account sheet contains a JSON textarea.
- [ ] Card header shows the key count and 「＋ 添加 key」. The page hint is present. The models hint no longer mentions `options.passthrough`.
- [ ] The model sheet has no JSON textarea. Recreating the README examples (seedance with `upstream_image` + `duration_format {n}s`, flux-pro with `upstream_edit`, eleven-v3 with voices) through the form stores the same options. Editing an existing model round-trips its values and keeps unknown keys.
- [ ] 「从平台获取」 → fal → 文生视频 → search `seedance` lists real endpoints. Ticking `bytedance/seedance-2.5/flash/text-to-video` and importing creates video model `seedance-2.5-flash` with `upstream_image`/`upstream_reference` auto-filled. Re-opening the list shows it as 已添加.
- [ ] ElevenLabs lists only TTS models (needs a key; without one the error says to add a key first). Fish Audio shows the built-in list with s1 marked 已弃用.
- [ ] New backend code has unit tests (fal/elevenlabs/fishaudio listers, admin route) and no API key appears in any response or log.
- [ ] `go vet ./...` and `go test ./...` pass. `node --check` passes on changed JS. The CSP grep (`innerHTML|onclick=|style=`) over `internal/web/assets` is empty.

## Out of Scope

- Backend changes other than the R9 model-list endpoint; no schema change; no batch-create endpoint.
- Fetching voices from platforms (user chose models only).
- Raw JSON editing anywhere in the admin UI (all backend-read keys are covered by controls; others stay API-only).

## Follow-up Scope Delivered (same session, user-requested)

- R10 UI polish: switch redesign (fixed size, centered knob, text labels, account rows use switches), login remembered by default, 供应商 → 平台 wording, fetch sheet defaults to a usable platform.
- R11 Infinite Canvas naming: Canvas guesses capability from the model name only (`guessCapability` in basketikun/infinite-canvas `web/src/stores/use-config-store.ts`); auto IDs get `-video` / `-tts` / `-image` suffixes and a warning is shown when the name will be misclassified.
- R12 Model rename: `PATCH /admin/models/{id}` accepts `id`; the rename rewrites `video_tasks.model_id` in the same transaction.
- R13 Gateway keys copyable: `api_keys.key_enc` (migration 003) + `GET /admin/keys/{id}/secret`; legacy hash-only keys cannot be recovered. The 试用 page authenticates with ADMIN_TOKEN (accepted on /v1) and no longer needs a gateway key.
- R14 Request log params: `request_logs.params` (migration 004) with summarized inputs (never bytes) and a Chinese detail sheet; logs are written only for the four POST generation endpoints.
- R15 Proxies: `proxy_url` on providers/accounts plus global `settings.proxy_url` (migration 005); precedence account > platform > global; values `""` inherit / `direct` / URL (http, https, socks5, socks5h); all upstream calls and downloads use the effective proxy; `POST /admin/proxy/test`.
- R16 Proxy pool: named `proxies` table (migration 006); levels can reference `proxy:<id>`; deleting a proxy that is in use → 409; dangling references error out instead of going direct.
