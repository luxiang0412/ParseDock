-- PR-06: durable per-owner storage accounting (bytes of live result artifacts).
CREATE TABLE IF NOT EXISTS owner_storage (
    owner_id TEXT PRIMARY KEY,
    bytes    INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);
