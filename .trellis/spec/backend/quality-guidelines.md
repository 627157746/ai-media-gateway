# Quality Guidelines

> Code standards, testing requirements, and forbidden patterns for this repo.

---

## Style

- Compact idiomatic Go; minimal comments — only where behavior is non-obvious (e.g. `finalErr`, `TransportError` Final semantics in `fal.go`/`httputil.go` are the model).
- **stdlib-first**: the only non-stdlib dependency is `modernc.org/sqlite` (pinned v1.62.0). Any new dependency needs a version published ≥7 days ago and a justification in the task.
- Routing is `net/http.ServeMux` method patterns (`"GET /v1/models"` style, see `server.go`, `openai.go` `New`). No router dependency.
- Inject the clock where time matters: `accounts.New(…, now func() time.Time)` — pool tests run on a fake clock (`pool_test.go`).

## Tests — httptest fake vendors, no real network

Every package that talks to a vendor uses an in-process `httptest.Server` fixture: `fal_test.go` `fakeQueue` (queue endpoints + scripted `statusSeq`/`statusHTTP`), `elevenlabs_test.go`, `fishaudio_test.go` (`captured` bodies), `openai_test.go` (fake adapters registered under test-only provider types + `accounts.Pool` over a temp-DB store). Never make real network calls or use real keys in tests.

| Test file | Covers |
|---|---|
| `secret_test.go` | Seal/Open round-trip, tamper detection |
| `store_test.go` | migrations (incl. 002 data-fix), CRUD, key auth, 409 delete-with-dependents, log prune |
| `accounts/pool_test.go` | priority groups, SWRR 2:1, group-resync on membership swap, Retry-After, backoff+cap, auth 24h, fail_count reset, Invalid/Final no-failover, `Result.AccountID` on failure paths, `NoAccountError`, allowlist, ctx cancel, `CallFor`, `MaxAttempts` |
| `accounts/resolve_test.go` | alias hit, wrong kind, passthrough on/off, not found |
| `server/server_test.go` | CORS preflight, `/v1` gateway-key auth, `/v1/models` shape |
| `admin/admin_test.go` | CRUD flows, key mask never leaks, unknown provider type 400, weight ≥ 1 validation |
| `provider/fal/fal_test.go` | queue flow, endpoint selection, field mapping, error classes, Final semantics, cancel PUT auth, transient-poll retry, own-timeout |
| `provider/elevenlabs|fishaudio/*_test.go` | headers, voice alias, format maps, speed, error classes |
| `openai/openai_test.go` | Canvas multipart fields, mode normalization, failover+task affinity, poll keep/fail policy, Range `/content`, b64_json, speech streaming, request_log fill (incl. `account_id` on failed requests) |

## Required verification commands

```powershell
go vet ./...
go test ./...
go build -o bin/gateway.exe ./cmd/gateway
```

`-race` needs cgo/gcc and is **unavailable on this machine** — don't rely on it; correctness under concurrency is pinned by `TestConcurrentDoRaceFree` + mutexes.

## Forbidden patterns

- **Failover after a successful submit** — retries may duplicate paid vendor jobs. Post-submit errors must be `Final` (`fal.go` `finalErr`, `TransportError(err, true)` dial-vs-post-dial). See `error-handling.md`.
- **Buffering whole media payloads** — audio/video stream through (`speech.go` flushes 32KB chunks via `ResponseController`; `videos.go` `/content` proxies the upstream body).
- **Server-side fetching of user-supplied `http(s)://` URLs** — SSRF. Input URLs pass straight to the vendor (`media.Input.URL` / `DataURL`); only vendor-returned result URLs are fetched (`provider.FetchURL`), plus `b64_json` conversion of results.
- **Returning plaintext keys** — account `api_key` is write-only (`api_key_masked` out); gateway keys are shown once at create and stored as SHA-256.
- Logging `Authorization`, keys, or bodies (see `logging-guidelines.md`).
