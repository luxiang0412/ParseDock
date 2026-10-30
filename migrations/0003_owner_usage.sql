-- PR-02: durable per-owner usage accounting.
--
-- Job rows are purged after the tombstone window (10 min by default), so they
-- cannot back a 24 h quota. This table is written at admission and pruned only
-- after it leaves the rolling window (25 h), independent of job cleanup.
CREATE TABLE IF NOT EXISTS owner_usage (
    job_id     TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS owner_usage_owner ON owner_usage(owner_id, created_at);
