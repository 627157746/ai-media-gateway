# Admin web UI

## Goal

Add an embedded web admin UI to the gateway, styled after magpie's GUI (yetone/magpie `internal/gui/assets`): vanilla HTML/CSS/JS, no build step, served via `go:embed` from `internal/web`, left sidebar with grouped nav, light/dark theme via CSS variables + `prefers-color-scheme`, state pills, side "sheet" for add/edit. **Simplified Chinese only** in the UI.

## Decisions (settled with user)

- Vanilla JS ES modules embedded in the binary — same approach as magpie. No npm, no build tooling, no new Go dependencies.
- Pages: 供应商 (providers+accounts), 模型 (models), API Key, 试用 (playground), 请求日志, 视频任务. Hash routing.
- Token auth: login screen asks for `ADMIN_TOKEN`, stored in sessionStorage (or localStorage when 记住登录). Gateway keys for the playground are user-supplied.

## Requirements

### Backend

- `internal/web`: embed `assets/`; `GET /` → index.html, `GET /assets/*` static with correct content types (`text/javascript` for .js), `Cache-Control: no-cache`; CSP on HTML: `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob: https: http:; media-src 'self' data: blob: https: http:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'` + `X-Content-Type-Options: nosniff`. No inline scripts/styles anywhere. Unknown non-API paths → 404. No auth on static.
- Mount in `internal/server`; `/v1` and `/admin` auth unchanged.
- New `GET /admin/provider-types` → `{"data":[...]}` (sorted `provider.Types()`).

### Frontend (`internal/web/assets/`)

`index.html`, `app.css`, `app.js`, `api.js`, `ui.js`, `pages/{providers,models,logs,tasks,keys,playground}.js`. DOM built via `h()`/`textContent` — never `innerHTML` with data.

- Login + token persistence + theme toggle (自动/浅色/深色).
- Providers page: `/admin/status` cards with accounts (state pills 正常/冷却至/已停用), priority/weight, counters, last_error; edit/reset/enable/delete sheets & confirms; add provider (type select from provider-types); auto-refresh 10s, paused while a sheet is open.
- Models page: grouped by kind; enabled toggle; sheet with provider select, kind, upstream, options JSON + 插入模板 presets per provider type/kind; passthrough note.
- Logs: `/admin/logs` filters (account, 1h/24h/7d, limit), attempts>1 highlight (failover), status colors, auto-refresh toggle.
- Tasks: `/admin/tasks` status filter, progress, link to video_url when completed.
- Keys: create → one-time plaintext display + copy + 「用于试用」; list last_used; delete.
- Playground: gateway key input (localStorage on action only), cost banner, model select by kind; image (generations / edits multipart), video (Canvas multipart field names, 3s polling, <video>), speech (blob → <audio> + download); show `error.message` inline.

## Acceptance Criteria

- [ ] `go vet ./...` and `go test ./...` pass (incl. new web/admin tests)
- [ ] `node --check` clean on every JS module
- [ ] No `innerHTML`/`onclick=`/inline `<script>`/`style=` in assets (CSP-enforced)
- [ ] Smoke run: pages served with CSP, seeded admin data visible, `/v1`+`/admin` auth still enforced
- [ ] Frontend specs under `.trellis/spec/frontend/` rewritten to match the real UI

## Notes

- Styling references: magpie `internal/gui/assets` conventions (sidebar groups, pill states, sheet pattern); CSS variables both themes.
- Canvas multipart field names must match `internal/openai/videos.go` (`first_frame`, `last_frame`, `resolution_name`, `image[]`, `video[]`, `audio[]`, ...).
