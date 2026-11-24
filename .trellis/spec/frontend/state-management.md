# State Management

> No framework — routing is `location.hash`, persistence is Web Storage, server state is fetched fresh. `internal/web/assets/app.js` owns the shell.

---

## Routing

- Hash router in `app.js`: `ROUTES` maps `#/providers` … to `{title, desc, mount}`; unknown hashes fall back to `#/providers`. `hashchange` → re-render shell + page.
- Page mounts return a cleanup function (`mountXxx() → () => void | null`) which `app.js` calls before rendering the next page — timers, pollers, aborts must be registered there (see `playground.js` `stopPoll` chain, `logs.js`/`providers.js` `clearInterval`).

## Persisted state (the only local state)

| Key | Where | Storage | Notes |
|---|---|---|---|
| `mgw_admin_token` | `api.js` | `sessionStorage`, or `localStorage` when 「记住登录」checked | `getToken()`/`saveToken(t, remember)`/`clearToken()`; a 401 on an `admin()` call clears it and reloads to login |
| `mgw_gateway_key` | `api.js` | `localStorage`, only on explicit user action (「保存」/「用于试用」) | `getGwKey()`/`saveGwKey()` — powers `v1()` calls in the playground |
| `mgw_theme` | `app.js` | `localStorage` | `auto` (default, `prefers-color-scheme`) / `light` / `dark`; applied as `data-theme` on `<html>` vs the CSS media query |

Everything else is fetched per page-mount — don't cache server data across pages or write app state into globals beyond module-local `let`.

## Server state & polling

- Pages re-fetch on mount and expose `reload()` closures to sheets/buttons.
- Auto-refresh timers must (a) be cleared in the returned cleanup, and (b) pause while a sheet is open — check `isSheetOpen()` from `ui.js` (see `providers.js` 10s loop; `logs.js` refreshes only when its own toggle is on).
- Video polling in the playground runs on a 3s interval tied to `stopPoll`, which is also chained when switching tabs — terminal states (`completed`/`failed`/`cancelled`) stop it.

## Auth flow

`boot()` in `app.js`: no token → login card (`renderLogin`); token → shell. Login validates via `GET /admin/status` before persisting. `v1()` 401s (bad gateway key) do NOT log out — only `admin()` 401 does (`api.js request()` `isAdmin` flag).
