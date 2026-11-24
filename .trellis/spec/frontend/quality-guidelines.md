# Quality Guidelines

> Hard rules for `internal/web/assets/` — most exist because of the CSP in `internal/web/web.go`.

---

## Forbidden (CSP / XSS — checked in review)

- `innerHTML` / `outerHTML` / `insertAdjacentHTML` — build DOM with `h()` + `textContent` only.
- Inline handlers (`onclick=`), inline `<script>`, `<style>` blocks, `style=` attributes — `script-src 'self'`, `style-src 'self'` reject them. Dynamic styling = add/remove classes; needed one-off styles get a utility class in `app.css` (`.flex-row`, `.mt14`, `.w320`, `.fs12`, `.lbl`, `.cell-err`, `.row.start`…).
- `alert()`/`confirm()` — use `toast`/`confirmDialog`/`modalDialog`.
- Third-party CDNs, icon fonts, external scripts — everything ships embedded; icons are self-authored SVG (`favicon.svg`).
- Storing the admin token or gateway key anywhere except the storage keys in `api.js` — and the gateway key only on explicit user action.

## Conventions

- **All UI text in Simplified Chinese** (「供应商」「＋ 添加账号」「冷却至 …」); code comments and identifiers in English.
- ES modules only (`type="module"`); relative imports; top-level `export function mount…` per page.
- Keep modules dependency-free — `app.js` imports page modules, pages import `api.js`/`ui.js`; pages must not import each other or `app.js`.
- Tables in `.table-wrap`, status via `pill()`, toggles via `switchEl`, forms via `field()` inside `openSheet` — see `component-guidelines.md`.
- Media shown via vendor `<video>`/`<audio>` after blob download (`createObjectURL`); images may hot-link upstream URLs (`img-src` allows `https: http: data: blob:`).
- Theme support is automatic via CSS vars — use only `var(--*)` colors, never hard-coded hex in components.

## Verification

```powershell
# syntax (copy to .mjs — Node treats .js as CJS; don't pipe via Get-Content, it corrupts UTF-8)
Get-ChildItem internal\web\assets -Recurse -Filter *.js | % { Copy-Item $_.FullName "$env:TEMP\$($_.BaseName).mjs"; node --check "$env:TEMP\$($_.BaseName).mjs" }
# CSP scan — must return nothing
Get-ChildItem internal\web\assets -Recurse -Include *.js,*.html,*.css | Select-String 'innerHTML|onclick=|style='
```

Plus `go test ./internal/web/` (serving, CSP header, 404s) and the smoke run: `GET /` → 200 + CSP + nosniff; `/assets/app.js` → `text/javascript`; unknown path → 404; `/v1`, `/admin` auth untouched.
