-- ParseDock Phase 0 schema (v1).
-- Two independent lifecycle dimensions: processing_status and retention_status.
-- All timestamps are absolute UTC RFC3339Nano strings.

CREATE TABLE IF NOT EXISTS jobs (
    id                  TEXT PRIMARY KEY,
    owner_id            TEXT NOT NULL,
    input_name          TEXT NOT NULL,
    mime_type           TEXT NOT NULL,
    format              TEXT NOT NULL,
    input_sha256        TEXT NOT NULL DEFAULT '',
    input_size          INTEGER NOT NULL DEFAULT 0,
    page_count          INTEGER NOT NULL DEFAULT 0,
    processing_status   TEXT NOT NULL,
    retention_status    TEXT NOT NULL DEFAULT 'active',
    priority            INTEGER NOT NULL DEFAULT 0,
    requested_mode      TEXT NOT NULL DEFAULT 'auto',
    requested_device    TEXT NOT NULL DEFAULT 'auto',
    engine_hint         TEXT NOT NULL DEFAULT 'auto',
    actual_engine       TEXT NOT NULL DEFAULT '',
    routing_reason      TEXT NOT NULL DEFAULT '',
    idempotency_key     TEXT,
    created_at          TEXT NOT NULL,
    started_at          TEXT,
    completed_at        TEXT,
    result_expires_at   TEXT,
    max_lifetime_at     TEXT NOT NULL,
    current_attempt     INTEGER NOT NULL DEFAULT 0,
    max_attempts        INTEGER NOT NULL DEFAULT 3,
    result_version      TEXT,
    result_manifest_path TEXT,
    error_code          TEXT,
    error_message       TEXT,
    warning_count       INTEGER NOT NULL DEFAULT 0,
    progress            INTEGER NOT NULL DEFAULT 0,
    progress_stage      TEXT NOT NULL DEFAULT '',
    requested_ttl_seconds INTEGER NOT NULL DEFAULT 0,
    available_at        TEXT NOT NULL DEFAULT '',
    tombstone_expires_at TEXT,
    updated_at          TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS jobs_idem
    ON jobs(owner_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS jobs_queue
    ON jobs(processing_status, priority DESC, created_at);
CREATE INDEX IF NOT EXISTS jobs_ttl
    ON jobs(retention_status, result_expires_at);
CREATE INDEX IF NOT EXISTS jobs_owner
    ON jobs(owner_id, created_at DESC);

CREATE TABLE IF NOT EXISTS attempts (
    job_id           TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    attempt_no       INTEGER NOT NULL,
    worker_id        TEXT NOT NULL,
    lease_token      TEXT NOT NULL,
    lease_until      TEXT NOT NULL,
    started_at       TEXT NOT NULL,
    last_heartbeat_at TEXT,
    state            TEXT NOT NULL,
    device_type      TEXT NOT NULL DEFAULT '',
    engine           TEXT NOT NULL DEFAULT '',
    error            TEXT,
    PRIMARY KEY (job_id, attempt_no)
);
CREATE INDEX IF NOT EXISTS attempts_state ON attempts(state, lease_until);

CREATE TABLE IF NOT EXISTS artifacts (
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    path       TEXT NOT NULL,
    size       INTEGER NOT NULL DEFAULT 0,
    sha256     TEXT NOT NULL DEFAULT '',
    generation INTEGER NOT NULL DEFAULT 1,
    ready_at   TEXT NOT NULL,
    PRIMARY KEY (job_id, kind)
);

CREATE TABLE IF NOT EXISTS download_leases (
    lease_id        TEXT PRIMARY KEY,
    job_id          TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    created_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    last_renewed_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS download_leases_job ON download_leases(job_id, expires_at);

CREATE TABLE IF NOT EXISTS job_events (
    event_id   INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    payload    TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS job_events_job ON job_events(job_id, event_id);

CREATE TABLE IF NOT EXISTS workers (
    worker_id      TEXT PRIMARY KEY,
    host           TEXT NOT NULL DEFAULT '',
    engines        TEXT NOT NULL DEFAULT '[]',
    device_type    TEXT NOT NULL DEFAULT 'cpu',
    gpu_model      TEXT NOT NULL DEFAULT '',
    gpu_memory_bytes INTEGER NOT NULL DEFAULT 0,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    memory_bytes   INTEGER NOT NULL DEFAULT 0,
    max_concurrency INTEGER NOT NULL DEFAULT 1,
    registered_at  TEXT NOT NULL,
    heartbeat_at   TEXT NOT NULL
);

-- Key/value for schema version and coordinator bookkeeping.
CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
