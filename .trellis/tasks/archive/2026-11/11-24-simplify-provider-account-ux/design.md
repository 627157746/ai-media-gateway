# Design: Simplify provider/account admin UX

## Boundaries

Frontend only: `internal/web/assets/pages/providers.js` (main), `internal/web/assets/pages/models.js` (form rewrite + hint text), `internal/web/assets/app.css` (a few classes). No Go changes. Existing endpoints are reused as-is.

## Helpers (page-local in providers.js)

```js
const TYPE_LABELS = { fal: 'fal', elevenlabs: 'ElevenLabs', fishaudio: 'Fish Audio' }; // fallback: raw type

// Returns a new object; patch values of undefined / '' / empty-object delete the key.
function mergeOpts(base, patch)

// Collapsible section: native <details><summary> (no inline script/style, CSP-safe).
function more(title, ...children) // → h('details', {class:'more'}, h('summary', {text:title}), ...)

// 音色设置 widget.
function voiceEditor(opts) // → { el, value(): { default_voice, voices } }

function parseKeys(text) // trim, drop blanks, dedupe, keep order

function autoID(type, providers) // type, else type-2, type-3 … not in providers ids

// Sequential POSTs; returns failed [{key, err}].
async function createAccounts(providerID, keys, common)
```

## Sheets

- **addPlatformSheet(providers)**: type select (labels via TYPE_LABELS), keys textarea, existing-type hint (`.hint`, updated on type change), `more('更多设置', ID, Base URL, passthrough switch, 启用)`. ID input value auto-tracks `autoID(type)` until the user edits it (dirty flag). Save: POST provider with `options:{passthrough}`; on error stop. Then `createAccounts`. Partial failure → close is skipped, the sheet switches to show the failed keys + error. Since the provider now exists, a retry must only add accounts: after a successful provider create, lock the provider fields and make re-submit call only `createAccounts` for the remaining keys.
- **providerEditSheet(p)**: Base URL, passthrough switch, 启用, `more('音色设置', voiceEditor)`. PATCH `{base_url, enabled, options: mergeOpts(p.options, {passthrough, ...voice.value()})}`.
- **accountSheet(p, existing)**: keys textarea (create) / password input (edit), 备注名, `more('更多设置', 优先级, 权重, Base URL, 启用, voiceEditor)`. Options always sent as `mergeOpts(existing?.options, voice.value())` (merge keeps unknown keys; for create the base is `{}`).

## Card layout

Header: id (title), type label pill, base_url (muted), `pill(N ? \`${N} 个 key 轮流使用\` : '还没有 key')`, spacer, enabled switch, 「＋ 添加 key」(btn sm primary), 编辑, 删除. Bottom add-account button removed. The page hint `.muted.fs12` sits above the list. Bottom button renamed 「＋ 添加平台」.

## CSS

`.more` (details block, margin), `.more > summary` (cursor pointer, muted, padding), `.kv-row` (flex, gap 8px, inputs flex 1). No inline styles.

## Compatibility / rollback

Stored data is untouched apart from merged keys the user edits. Rollback = revert the three asset files.

## Model sheet (models.js)

- Shared widgets move to `ui.js` so both pages use them: `mergeOpts`, `more`, `voiceEditor`, plus `kvRows({rows, keyOptions?, keyPlaceholder, valPlaceholder})` → `{el, value(): object}` (the key is a select when `keyOptions` is given, else free text).
- `FAL_FIELD_KEYS = { image: [prompt,count,ratio,size,resolution,quality,background,images,mask], video: [prompt,duration,ratio,resolution,generate_audio,watermark,first_frame,last_frame,references,videos,audios], speech: [text,voice,speed] }`.
- Sections rebuild on 平台/类型 change. Values typed so far are carried by reading the current widget values into a working options object before the rebuild.
- `fields` = kvRows value + size_mode/duration_format selects (default choice → key deleted). `defaults` value parse: `try JSON.parse` → use if not object/array-less-string ambiguity, else the string. `drop` = split on comma/whitespace, trimmed, non-empty; empty → delete.
- 限定账号 loads `GET /admin/providers/{id}/accounts` for the selected platform.
- Save: `options = mergeOpts(existing?.options, collected)`; remove `TEMPLATES`, `mergeOpts` (local) and the `parseJSONField` usage.

## R9 Fetch models from the platform

### Backend contract

`internal/provider/provider.go`:
```go
type ModelQuery struct { Q, Category, Cursor string; IDs []string }
type UpstreamModel struct {
    ID          string     `json:"id"`          // upstream id / endpoint_id
    Name        string     `json:"name"`
    Kind        media.Kind `json:"kind"`        // image|video|speech; non-importable entries are dropped
    Category    string     `json:"category"`
    Description string     `json:"description,omitempty"`
    Deprecated  bool       `json:"deprecated,omitempty"`
}
type ModelPage struct { Models []UpstreamModel; NextCursor string }
type ModelLister interface { ListModels(ctx context.Context, c Call, q ModelQuery) (ModelPage, error) }
```
- fal: `var catalogBase = "https://api.fal.ai/v1"` (a var for tests). `GET /models?limit=50&status=active` plus q/category/cursor/endpoint_id (repeated). `Authorization: Key` when an APIKey is present. Category → kind: text-to-image, image-to-image → image; text-to-video, image-to-video → video; text-to-speech → speech; everything else is dropped. Response `{models:[{endpoint_id, metadata:{display_name, category, description, status}}], next_cursor, has_more}`.
- elevenlabs: `GET {c.BaseURL(default)}/v1/models` with `xi-api-key`; keep `can_do_text_to_speech`; Q = case-insensitive substring on id/name; Category `text-to-speech`.
- fishaudio: static list s2.1-pro (推荐), s2.1-pro-free, s3-pro, drama-3, s2-pro, s1 (Deprecated); Q filter.
- admin: `admin.New(st, box, client)` (update main.go + 2 tests). `GET /admin/providers/{id}/upstream-models` → `{"data":[...],"next_cursor":""}`. Account: the highest-priority enabled account that has a key, preferring non-cooling ones. With no key: fal proceeds anonymously; others → 400 「该平台还没有可用的 key，请先添加」. An adapter that is not a ModelLister → 400. Upstream failure → error status with the upstream message, never the key.

### Frontend (models.js)

- 「从平台获取」 next to 「＋ 添加模型」 → sheet: 平台 select; fal-only 分类 select (文生图 text-to-image [default], 图生图/编辑 image-to-image, 文生视频 text-to-video, 图生视频 image-to-video, 文生语音 text-to-speech); search input (Enter or 搜索). Result rows: checkbox, name, mono id, kind pill, 已弃用 pill, 已添加 (disabled) when a model with the same provider+upstream exists; 加载更多 when next_cursor. A checked row reveals an editable ID input. Footer 「添加选中的 N 个」.
- Auto ID: lowercase; drop the `fal-ai/` prefix, or for other multi-segment ids drop the owner segment; drop a trailing task segment (text-to-image, image-to-image, text-to-video, image-to-video, reference-to-video, text-to-speech, edit); join with `-`; replace chars outside `[a-z0-9.-]` with `-`; collapse/trim dashes; Fish Audio ids get a `fish-` prefix; dedupe with `-2`, `-3`… against existing models and other selections. Must match `^[a-z0-9][a-z0-9.-]*$`.
- fal sibling linking before create: for `P/text-to-video` → candidates `P/image-to-video`, `P/reference-to-video`; for a text-to-image `E` → `E/edit`, plus `P/edit` and `P/image-to-image` when E ends in `/text-to-image`. Check them in one find-mode call (endpoint_id list, ≤50 per call) and set upstream_image / upstream_reference / upstream_edit for those that exist.
- Create sequentially via `POST /admin/models`. Partial failure: keep failed rows checked, show 「X 个失败：<first error>」, reload the list.
