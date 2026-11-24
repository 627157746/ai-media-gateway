# Implement: Simplify provider/account admin UX

## Checklist

1. `providers.js` helpers: `TYPE_LABELS`, `mergeOpts`, `more`, `voiceEditor`, `parseKeys`, `autoID`, `createAccounts`.
2. `providers.js` `addPlatformSheet` (one-step add, auto ID, existing-type hint, partial-failure retry that only re-adds keys).
3. `providers.js` `providerEditSheet` (passthrough switch, voice section, no JSON).
4. `providers.js` `accountSheet` (batch keys, 更多设置, voice section, no JSON).
5. `providers.js` card/render: key-count pill, header 「＋ 添加 key」, page hint, 「＋ 添加平台」; drop the unused `parseJSONField` import.
6. `ui.js`: shared `mergeOpts`, `more`, `voiceEditor`, `kvRows`. `models.js`: form rewrite per design (R8), remove templates/JSON, passthrough hint text.
7. `app.css`: `.more`, `.more > summary`, `.kv-row`.

## Validation

- `go vet ./...` ; `go test ./...`
- `node --check` on copies of changed JS renamed to `.mjs`
- CSP grep: `rg -n "innerHTML|onclick=|style=" internal/web/assets` → empty
- Smoke: `go build -o bin/gateway.exe ./cmd/gateway`, run with `ADMIN_TOKEN`/`MASTER_KEY` and a temp `DB_PATH`, then walk the acceptance criteria in the browser and check stored options via `GET /admin/providers/{id}` / `/admin/accounts/{id}`.

## Risk points

- Option merge losing keys → verify with a `{"foo":1}` round-trip (seed via API).
- One-step add partial failure → retry must not re-create the provider (review the code path).
