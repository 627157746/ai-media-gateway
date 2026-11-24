# Directory Structure

> `internal/web/` — embedded admin SPA. Everything browser-side lives under `internal/web/assets/` and ships inside the Go binary.

---

## Layout

```
internal/web/
├── web.go                 # //go:embed assets → Handler(): "/", "/assets/*", CSP/nosniff/no-cache, 404s
├── web_test.go
└── assets/
    ├── index.html         # shell only: <div id="app">, css link, <script type="module" src="/assets/app.js">
    ├── favicon.svg        # inline-authored SVG logo (no third-party icons)
    ├── app.css            # ALL styles: theme vars (light/dark/auto) + component classes + utilities
    ├── api.js             # fetch wrappers: admin(), v1(), token storage, ApiError
    ├── ui.js              # DOM helpers: h(), field(), pill(), switchEl, sheet/dialog/toast, time utils
    ├── app.js             # entry: hash router, NAV/ROUTES tables, login screen, layout, theme toggle
    └── pages/
        ├── providers.js   # #/providers — /admin/status cards + accounts CRUD, 10s auto-refresh
        ├── models.js      # #/models — grouped by kind, options JSON + 插入模板 presets
        ├── keys.js        # #/keys — create → one-time plaintext modal + 「用于试用」
        ├── playground.js  # #/playground — image/video/speech tabs calling /v1 with a gateway key
        ├── logs.js        # #/logs — /admin/logs table + filters + auto-refresh toggle
        └── tasks.js       # #/tasks — /admin/tasks status filter
```

## Rules

- **One module per page** under `pages/`, exporting `mountXxx(root) → cleanupFn | null`. `app.js` `ROUTES` maps the hash to `{title, desc, mount}` — add a row there and a `NAV` entry when adding a page.
- Imports are relative (`'../api.js'`, `'./ui.js'`); no bare specifiers — there's no bundler.
- New static files (images/fonts) go in `assets/` and are automatically embedded — keep them self-authored (no external icon/icon-font CDNs; CSP `img-src` allows `self data: blob: https: http:`).
- `index.html` stays a shell: never add markup for a page there — pages render into `#app`.
- Serving rules in `web.go`: only `/`, `/index.html`, `/assets/*` resolve; anything else 404s. HTML gets CSP + nosniff; all assets get `Cache-Control: no-cache`.
