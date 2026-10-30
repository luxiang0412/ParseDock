-- PR-02: correlate a job with the client request that created it.
ALTER TABLE jobs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS jobs_owner_created ON jobs(owner_id, created_at);
