# API Client

> `internal/web/assets/api.js` — the single fetch layer. Two APIs: `/admin/*` (Bearer ADMIN_TOKEN) and `/v1/*` (Bearer gateway key).

---

## Surface

- `admin(path, opts)` → parsed JSON; `v1(path, opts)` for gateway calls.
- `opts.json` → sets `Content-Type: application/json` + `JSON.stringify` body. Pass a `FormData` `body` directly for multipart (do **not** set Content-Type — the browser adds the boundary; see `playground.js` image/video submits using Canvas field names `first_frame`/`last_frame`/`resolution_name`/`image[]`).
- `ApiError` — `err.status` + `err.body` (the OpenAI `{error:{message,type,code}}` shape); `err.message` is already `error.message` when present, so `errMsg(e)` (ui.js) prints what the user should see.
- `validateToken(token)` → `GET /admin/status` without persisting (login screen).
- Streaming/binary responses can't go through `request()` — `playground.js` fetches `/v1/audio/speech` and `/v1/videos/{id}/content` directly with `fetch` + `Authorization` header and reads `.blob()` → `URL.createObjectURL` (media elements can't carry auth headers).

## Behaviors to preserve

- **401 on `admin()` → session reset**: `request()` clears the stored token and `location.reload()`s back to the login screen (the `isAdmin` flag restricts this to admin calls — a bad gateway key in the playground must not log the admin out).
- Non-JSON responses are returned as text; errors always throw `ApiError`.
- Endpoints used today: `/admin/status` (providers+accounts in one call), `/admin/provider-types`, `/admin/providers*` , `/admin/providers/{id}/accounts`, `/admin/accounts/{id}[/reset]`, `/admin/models*`, `/admin/keys*`, `/admin/tasks?status=&limit=`, `/admin/logs?account=&since=<unix-ms>&limit=`, `/v1/models`, `/v1/images/generations|edits`, `/v1/videos`, `/v1/audio/speech`.

## Adding a call

- New admin endpoint → add a tiny function or call `admin(path, {method, json})` inline in the page; keep the OpenAI-error → `ApiError` path untouched.
- Never hand-roll `fetch` for JSON admin calls — only binary/streaming responses justify direct `fetch` (document why at the call site like `playground.js` does).
