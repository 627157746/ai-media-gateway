CREATE TABLE voices (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    upstream_id TEXT NOT NULL,
    options     TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX idx_voices_provider ON voices(provider_id);
CREATE UNIQUE INDEX idx_voices_upstream ON voices(provider_id, upstream_id);
