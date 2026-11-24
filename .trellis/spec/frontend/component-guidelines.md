# Component Guidelines

> Page modules & DOM helpers — `internal/web/assets/ui.js` is the toolkit; pages are functions, not classes.

---

## The `h()` builder

Every element is created with `h(tag, attrs, ...children)` (`ui.js`):

- `attrs`: `class`, `text` (→ `textContent`), `on<event>` → `addEventListener` (e.g. `onclick`, `onchange`), `value`, `checked`, `disabled`, everything else → `setAttribute`.
- children accept elements, strings, arrays — `null`/`false`/`undefined` are skipped (use for conditional nodes: `cond ? h(...) : null`).
- Data from the server goes **only** through `text` / `textContent` — never `innerHTML` (XSS + CSP). Show JSON blobs via `JSON.stringify`, never by injecting markup.

Example pattern (`pages/providers.js` `accountRow`): build a row, append conditional parts (`a.last_error ? h(...) : null`), return the element.

## Standard components (reuse, don't re-invent)

| Helper | Use |
|---|---|
| `field(label, input, hint?)` | form row in sheets — label above, optional hint below |
| `pill(text, kind)` | status badges — kinds `ok`/`warn`/`bad`/`dim`/`acc` (see `statePill` in providers.js, `KIND` map in tasks.js) |
| `switchEl(checked, onChange)` | enabled toggles — call PATCH then `reload()` (providers.js, models.js) |
| `openSheet(title, contentEl)` | all add/edit UIs — right-side panel; returns `close()`; tracks `isSheetOpen()` which auto-refresh loops respect |
| `sheetButtons(text, onSubmit, close)` | 取消/保存 row at sheet bottom |
| `confirmDialog(msg, okText)` | every destructive action (delete provider/account/model/key) — shows the backend message on failure via `errMsg(e)` |
| `modalDialog(title, contentEl)` | one-time key display (`keys.js` `showKeyOnce`) |
| `toast(msg, kind)` | transient feedback — `'bad'` lingers 6s |
| `relTime`/`fmtTime`/`copyText`/`parseJSONField` | times, clipboard, options-JSON validation (returns `{ok, value|error}` — surface `error` in the sheet's `.err-text`) |

## Page structure

Each page module follows the same shape (`pages/logs.js` is the cleanest example):

```js
export function mountX(root) {
  const listEl = h('div');
  root.append(toolbarEls, listEl);
  async function load() { /* admin(...) → render(listEl, data) or .err-text */ }
  load();
  const timer = setInterval(...);        // if the page polls
  return () => clearInterval(timer);     // cleanup on route change
}
```

- Rebuild the list DOM on every refresh (`listEl.textContent = ''` then re-append) — state lives in the store/API, not the DOM.
- Keep `reload`/`load` closures passed into sheets so saves refresh the page.
- Errors render into `.err-text`/`toast`, never `alert()`.
- Tables go in `.table-wrap` (horizontal scroll); group headers use `tr.kind-head` (models.js).

## Adding UI

- New page → `pages/<name>.js` + `ROUTES`/`NAV` entry in `app.js`.
- New shared widget → `ui.js`; page-local widget stays in the page module.
- New style → `app.css` class. **Never** a `style` attribute (CSP) — add a utility class in the `/* utilities */` block instead (`.flex-row`, `.mt14`, `.w320`, `.fs12`…).
