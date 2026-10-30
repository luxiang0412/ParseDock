-- PR-06: durable per-owner storage accounting (bytes of live result artifacts).
CREATE TABLE IF NOT EXISTS owner_storage (
    owner_id TEXT PRIMARY KEY,
    bytes    INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL
);

-- Backfill from artifacts that already exist (upgrade from a pre-PR-06 database),
-- so old results consume quota and their release cannot under-count new ones.
INSERT INTO owner_storage(owner_id, bytes, updated_at)
SELECT j.owner_id, SUM(a.size), strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM artifacts a JOIN jobs j ON j.id = a.job_id
GROUP BY j.owner_id
ON CONFLICT(owner_id) DO UPDATE SET bytes = excluded.bytes, updated_at = excluded.updated_at;
