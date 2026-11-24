# Database Guidelines

> SQLite (`internal/store`), pure-Go driver `modernc.org/sqlite` **v1.62.0** — the only non-stdlib dependency.

---

## Connection setup (`internal/store/store.go` `Open`)

- `database/sql` + `modernc.org/sqlite` driver name `sqlite`; `SetMaxOpenConns(1)` — serializes writes, avoids `SQLITE_BUSY` races.
- Pragmas set on open: `journal_mode=WAL`, `busy_timeout`, `foreign_keys=ON`. Reuse `Open`; do not open a second `sql.DB`.

## Migrations (`internal/store/migrations/`)

- SQL files embedded via `embed.FS` (`migrationsFS`), named `NNN_<name>.sql`; the numeric prefix is the version. `schema_version` table holds the applied version; `Store.migrate` runs pending files in order, each in a transaction.
- **Adding one**: create `00N_<name>.sql` with the next number and bump nothing else. Never edit a migration that has shipped — deployed DBs will not re-run it. Data-fix migrations are fine: `002_weight_min.sql` (`UPDATE accounts SET weight = 1 WHERE weight < 1;`) repaired legacy rows after the admin API started enforcing `weight >= 1` (`TestMigrationWeightMin`).
- Tables: `providers`, `accounts`, `models`, `api_keys`, `video_tasks`, `request_logs` (`001_init.sql`).

## Conventions

- **Times** are stored as unix-ms `INTEGER` (`nowMS`, `timeFromMS`, `msOrNil` in `store.go`); nullable times go through `sql.NullInt64` → `*time.Time`.
- **JSON options** are `TEXT` columns scanned into `[]byte` and assigned to `json.RawMessage` fields — Go 1.27's `RawMessage` (jsontext.Value) is not a `sql.Scanner`, so do not scan into it directly (see `scanProvider`/`optsOrEmpty`).
- **Sentinels**: `store.ErrNotFound` (mapped from `sql.ErrNoRows`, also returned by `oneRow` on `RowsAffected()==0`), `store.ErrConflict` (duplicate id / delete-with-dependents). Handlers map them to 404/409 in `httpx.WriteError`.
- **No cascade deletes**: `DeleteProvider` checks accounts+models and returns `ErrConflict` → 409 (`providers.go`, tested in `store_test.go`).

## Secrets at rest

- Account API keys: column `api_key_enc` holds ciphertext only — sealed with `secret.Box` (AES-256-GCM, key = SHA-256 of `MASTER_KEY`) **by the caller**, not the repo: `admin` seals on create/PATCH, `accounts.Pool` opens when building `provider.Call` (`pool.go` `callFor`). Repos never see plaintext; admin GETs return `api_key_masked` (`first4…last4`).
- Gateway keys (`api_keys` table): only the SHA-256 hex is stored (`keys.go`); plaintext `sk-mg-<32 hex>` is returned once on create. `AuthenticateKey` hashes the presented key and bumps `last_used_at`.

## request_logs

One row per `/v1/*` request (inserted by `server` middleware after the handler returns). `PruneRequestLogs(olderThan)` runs hourly in `cmd/gateway/main.go` with `LOG_RETENTION` (default 720h).

## Where tests are

`internal/store/store_test.go` — migrations on temp DB, CRUD, key auth round-trip, 409 on provider delete, log prune.
