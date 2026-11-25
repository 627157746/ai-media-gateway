# Directory Structure

> How backend code is organized in this project.

---

## Layout

```
ai-media-gateway/
├── cmd/gateway/main.go          # wiring only: config → store → pool → server
├── internal/
│   ├── media/media.go           # vendor-neutral types shared by all layers
│   ├── config/config.go         # env parsing, required-var startup failure
│   ├── secret/secret.go         # AES-256-GCM Box (Seal/Open) for account keys
│   ├── store/*.go + migrations/ # SQLite repos, embedded SQL migrations
│   ├── provider/*.go            # Call/Adapter interfaces, registry, shared helpers
│   ├── provider/fal/            # fal.ai queue adapter
│   ├── provider/elevenlabs/     # ElevenLabs adapter
│   ├── provider/fishaudio/      # Fish Audio adapter
│   ├── accounts/                # pool.go (failover), resolve.go (model resolution)
│   ├── httpx/httpx.go           # OpenAI-shaped errors, WriteJSON, request-log ctx
│   ├── openai/*.go              # /v1 media endpoint handlers
│   ├── server/*.go              # mux, middleware (auth, CORS, logging, request_log)
│   ├── web/                     # embedded admin SPA: web.go (go:embed) + assets/ (vanilla JS)
│   └── admin/admin.go           # /admin/* JSON API
├── internal/store/migrations/001_init.sql
└── Dockerfile, docker-compose.yml, .env.example, README.md (Chinese), AGENTS.md
```

## Dependency direction

`media` is the leaf — everything may import it, it imports nothing internal.

- `provider` imports `media` only. Adapters (`provider/fal`, …) import `provider` + `media`.
- `accounts` imports `store`, `secret`, `provider`, `media`, `config`.
- `httpx` imports `media`, `accounts`, `store` (error mapping + `*store.RequestLog` in ctx). It exists specifically so `server`, `admin`, and `openai` can share JSON/error helpers **without** importing `server` — `openai` must never import `server` (import cycle).
- `server` imports `httpx`, `store`, `config`; mounts the `openai` handler passed in as a dep.
- `admin` imports `httpx`, `store`, `secret`, `provider`.
- `openai` imports `httpx`, `accounts`, `provider`, `store`, `media`.
- `web` imports only stdlib (`embed`, `fs`, `mime`, `http`) — it is mounted by `server` at `/` with no auth; its static pages call `/admin/*` which enforces the token.
- `cmd/gateway` imports everything and blank-imports the adapters:
  `_ "ai-media-gateway/internal/provider/fal"` etc.

When adding code, keep the direction acyclic: put shared HTTP helpers in `httpx`, shared media types in `media`, never add upward imports.

## Tests

`*_test.go` files sit next to the code in the same package (white-box; `admin_test.go`/`server_test.go`/`openai_test.go` use the same-package style too — `openai_test.go` registers fake adapters under test-only types). All vendor tests use `httptest` fake servers — no real network.

## Adding a new vendor

1. New package `internal/provider/<type>/` with an `Adapter` type implementing whichever of `provider.ImageGenerator` / `VideoGenerator` / `SpeechGenerator` it supports.
2. `func init() { provider.Register("<type>", Adapter{}) }` — admin rejects unknown provider types via `provider.Get` (`internal/admin/admin.go`).
3. Blank-import the package in `cmd/gateway/main.go`.
4. Classify upstream errors into `media.ErrorClass` per `error-handling.md` (use `provider.TransportError`, `provider.ReadErrorBody`, `provider.ParseRetryAfter` from `internal/provider/httputil.go`).
5. httptest fake-vendor tests in the package (see `fal_test.go` for the pattern).
6. Add a model-registration example to `README.md`.
