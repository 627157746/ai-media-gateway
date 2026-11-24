CREATE TABLE providers (
    id         TEXT PRIMARY KEY,
    type       TEXT NOT NULL,
    base_url   TEXT NOT NULL DEFAULT '',
    options    TEXT NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE accounts (
    id             TEXT PRIMARY KEY,
    provider_id    TEXT NOT NULL REFERENCES providers(id),
    name           TEXT NOT NULL DEFAULT '',
    api_key_enc    BLOB,
    base_url       TEXT,
    options        TEXT NOT NULL DEFAULT '{}',
    priority       INTEGER NOT NULL DEFAULT 0,
    weight         INTEGER NOT NULL DEFAULT 1,
    enabled        INTEGER NOT NULL DEFAULT 1,
    cooldown_until INTEGER,
    fail_count     INTEGER NOT NULL DEFAULT 0,
    success_count  INTEGER NOT NULL DEFAULT 0,
    failure_count  INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    last_error_at  INTEGER,
    last_used_at   INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE INDEX idx_accounts_provider ON accounts(provider_id);

CREATE TABLE models (
    id          TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    kind        TEXT NOT NULL CHECK (kind IN ('image', 'video', 'speech')),
    upstream    TEXT NOT NULL,
    options     TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX idx_models_provider ON models(provider_id);

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',
    key_hash     TEXT NOT NULL UNIQUE,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);

CREATE TABLE video_tasks (
    id              TEXT PRIMARY KEY,
    model_id        TEXT NOT NULL,
    provider_id     TEXT NOT NULL,
    account_id      TEXT NOT NULL,
    upstream_handle TEXT NOT NULL,
    status          TEXT NOT NULL,
    progress        INTEGER NOT NULL DEFAULT 0,
    result_url      TEXT,
    error           TEXT,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    completed_at    INTEGER,
    last_polled_at  INTEGER
);
CREATE INDEX idx_video_tasks_status ON video_tasks(status);

CREATE TABLE request_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    endpoint    TEXT NOT NULL,
    model_id    TEXT,
    provider_id TEXT,
    account_id  TEXT,
    attempts    INTEGER NOT NULL DEFAULT 0,
    status      INTEGER NOT NULL,
    latency_ms  INTEGER NOT NULL,
    error       TEXT
);
CREATE INDEX idx_request_logs_at ON request_logs(at);
