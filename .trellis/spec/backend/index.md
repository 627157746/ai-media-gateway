# Backend Development Guidelines

> Conventions for the AI media gateway backend — a single Go 1.27 service (`cmd/gateway`) that fronts fal / ElevenLabs / Fish Audio behind an OpenAI-compatible `/v1` API with multi-account failover, backed by SQLite.

---

## Overview

The codebase is one Go module, `ai-media-gateway`. Vendor-neutral types live in `internal/media`; vendor adapters register themselves under `internal/provider/<type>`; the multi-account pool and model resolver are `internal/accounts`; the external contract is `internal/openai` (client-facing `/v1`) and `internal/admin` (`/admin` management API); `internal/server` is middleware + mux; `internal/httpx` holds shared JSON/error helpers to keep `openai` free of `server` imports. Only non-stdlib dependency: `modernc.org/sqlite`.

When making changes, read the guides below that match the layer you touch — error semantics (`Final`, classes) and pool behavior are the easiest things to get subtly wrong.

---

## Guidelines Index

| Guide | Description | Status |
|-------|-------------|--------|
| [Directory Structure](./directory-structure.md) | Package layout, dependency direction, "add a vendor" checklist | Filled |
| [Database Guidelines](./database-guidelines.md) | SQLite setup, migrations, secrets at rest, sentinels | Filled |
| [Error Handling](./error-handling.md) | `UpstreamError` classes, `Final`, vendor status tables, `httpx` HTTP mapping | Filled |
| [Account Pool](./account-pool.md) | Eligibility, SWRR, cooldowns, task affinity, the fail-over window rule | Filled |
| [OpenAI API Contract](./openai-api.md) | `/v1` endpoints, Canvas fields, task object, auth/CORS, admin surface | Filled |
| [Provider Adapters](./provider-adapters.md) | fal queue protocol + options schema, ElevenLabs/Fish mapping, voice chain | Filled |
| [Logging Guidelines](./logging-guidelines.md) | slog fields, request_logs pipeline, levels | Filled |
| [Quality Guidelines](./quality-guidelines.md) | Style, test fixture patterns, verification commands, forbidden patterns | Filled |

---

**Language**: All documentation should be written in **English**.
