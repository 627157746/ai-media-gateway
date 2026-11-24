# Frontend Development Guidelines

> Conventions for the embedded admin UI in `internal/web/assets/` — vanilla HTML/CSS/JS ES modules, no build step, Simplified Chinese UI.

---

## Overview

The admin UI is a single-page app embedded in the Go binary via `//go:embed` (`internal/web/web.go` serves it, no auth — data calls hit `/admin/*` which enforces the token). It is written in **vanilla JS ES modules** (`<script type="module">`), styled after magpie's GUI: left sidebar with grouped nav, CSS-variable themes, state pills, side "sheet" panels. All UI text is **Simplified Chinese**. There is deliberately no framework, bundler, or npm dependency.

Key constraints that shape every file:

- **CSP forbids inline anything**: `script-src 'self'`, `style-src 'self'` — no inline `<script>`, `<style>`, `onclick=`, or `style=` attributes (set classes; a utility class lives in `app.css`). Verify: `Select-String -Pattern 'innerHTML|onclick=|style=' internal/web/assets -Recurse` must be empty.
- **XSS**: server/user data goes through `h()`/`textContent` only — never `innerHTML`.
- Syntax check JS with `node --check` (copy to `*.mjs` first so Node treats it as a module — PowerShell `Get-Content` corrupts UTF-8 into the pipe, don't use stdin).

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | asset files, page modules, serving rules | Filled |
| [Component Guidelines](./component-guidelines.md) | `h()` builder, sheet/dialog/toast/pill patterns | Filled |
| [State Management](./state-management.md) | hash routing, storage keys, page lifecycle/polling | Filled |
| [API Client](./api-client.md) | `api.js` fetch wrappers, auth handling, error shape | Filled |
| [Quality Guidelines](./quality-guidelines.md) | CSP/XSS rules, i18n, styling conventions, checks | Filled |
