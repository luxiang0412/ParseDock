# SLO and observability

> Scope: one Coordinator (SQLite + local disk), N stateless API replicas, M workers.
> Numbers below are the Phase-1 targets, and each one names the metric that measures it
> in `/internal/metrics` (see §2). Where a target is not yet backed by production data,
> that is stated instead of guessed.

## 1. Service level objectives

| SLO | Target | Measured by | Notes |
| --- | --- | --- | --- |
| API availability (create + status) | 99.5 % monthly | `up{job="parsedock-coordinator"}`, 5xx rate | excludes planned coordinator restarts (see `docs/RUNBOOK.md`) |
| Accepted-job completion | 99 % of jobs reaching a terminal state | `parsedock_jobs{state="failed"}` / total | engine-level failures count; `unsupported_type` is a client error and is excluded |
| Queue latency (fast mode, p95) | < 5 s at ≤ 10 jobs/s | `parsedock_claim_wait_seconds` | measured from creation to *first* claim |
| Parse latency (fast PDF, p95) | < 2 s, single page | `parsedock_parse_seconds{engine="xberg"}` | real-corpus p50 268 ms / p95 1.4 s at 4 workers |
| Parse latency (accurate, p95) | < 20 s, ≤ 10 pages | `parsedock_parse_seconds{engine="docling-rs"}` | model load dominates; 4 s median on 1-page fixtures |
| Throttling | < 1 % of requests at the documented rate limit | `parsedock_throttled_total` | a rising share means a client is polling instead of using SSE (RC-5) |
| Cleanup health | 0 failing passes over 15 min | `parsedock_cleanup_passes_total{outcome="error"}` | retention cannot advance while this fires |

### Result-loss budget

ParseDock deletes results on ACK **by design**: an ACK is a deletion request, so "zero loss
for acknowledged results" is not a meaningful target (PR-00 / F10).

The honest budget is for results that are **committed, not yet expired and not yet
ACKed**:

* **Target: 0** of those results may be lost inside the supported failure model, which is
  "the coordinator process dies and is restarted on the same host".
  Files are committed before the row leaves `running`, the row and artifact metadata commit
  in one transaction, and a restart re-reads both. `TestCompleteIsAtomic`-style paths plus
  the backup/restore drill cover it.
* **Out of budget:** loss of the coordinator's disk (single point of failure — back up or
  accept it), and anything deleted after an ACK.

## 2. Metric surface

All series come from `/internal/metrics` on the **internal** listener (internal token
required; the worker token is refused with `403`). Labels are deliberately low-cardinality: no owner, job id or file name ever
appears.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `parsedock_jobs_created_total` | counter | `mode`, `format` | jobs accepted |
| `parsedock_jobs` | gauge | `state` | jobs by processing status (scrape-time query) |
| `parsedock_retention` | gauge | `state` | jobs by retention status |
| `parsedock_queue_depth` | gauge | – | queued jobs |
| `parsedock_oldest_queued_seconds` | gauge | – | age of the oldest queued job |
| `parsedock_claim_wait_seconds` | histogram | – | creation → first claim (retries excluded) |
| `parsedock_parse_seconds` | histogram | `engine` | claim → committed result |
| `parsedock_result_bytes_total` | counter | `engine` | committed artifact bytes |
| `parsedock_workers_online` | gauge | `device` | workers with a heartbeat in 3 min |
| `parsedock_leases_expired_total` | counter | – | attempts lost to lease expiry |
| `parsedock_cleanup_passes_total` | counter | `outcome`=`ok|error` | cleanup passes |
| `parsedock_throttled_total` | counter | `reason`=`rate_limited|quota_exceeded|insufficient_storage` | refusals (RC-5); `rate_limited` also counts the per-address refusals after repeated failed authentication, so a spike with no matching owner traffic is usually a credential-guessing client |
| `parsedock_disk_free_bytes` | gauge | – | free bytes on the data volume |

API replicas are stateless and have no scrape endpoint. They batch their refusals and post
them to `POST /internal/throttle` every 5 s (best effort: a failed post is merged back, so a
coordinator restart loses no count), which is why `parsedock_throttled_total` is aggregated
centrally instead of per replica.

`deploy/observability/dashboard.json` (Grafana) and `deploy/observability/alerts.yaml`
(Prometheus) use exactly these names; `tests/integration/pr09_metrics.py` asserts the
series exist, that a rate-limited request is counted, and that no content, file name or
owner leaks into the exposition.

## 3. Tracing and correlation

There is **no OpenTelemetry SDK** in the binary: it would add a dependency we would have to
audit for the licence gate, and the useful correlation already exists.

* `X-Request-Id` is validated (`[A-Za-z0-9._:-]`, ≤ 128 chars), generated when absent, echoed
  to the client and stored on the job (`jobs.request_id`).
* Every audit line (`audit=true`) carries `request_id`, `owner_id`, `job_id`, sizes and the
  decision, never document text or tokens.
* `job_events` records `created → claimed → completed/failed/cancelled` with timestamps, so a
  single job's timeline is queryable without a trace backend.

Wiring an OTLP exporter later is additive: the propagation point is the request context in
`internal/api` and `internal/coordinator`.

## 4. Capacity signals (measured)

| Signal | Measured | Source |
| --- | --- | --- |
| Throughput (2 workers, 200 ms parse) | 8.8 jobs/s (88 % of the 10 jobs/s ceiling) | `docs/CLUSTER-LITE.md` §7 |
| Real corpus (972 files, 4 slots) | 3.6 files/s at client c=16 (slots 90 % busy); 2.3 files/s at c=4, where the 500 ms client poll is the limit | `docs/REAL-CORPUS-TEST.md` §2a |
| Disk per result | ≈ 1.9 × input bytes | `docs/DEPLOYMENT.md` §6a |
| Peak RSS | xberg ≈ 40 MB, docling-rs ≈ 0.9–1.2 GB | `docs/DEPLOYMENT.md` §6a |

Scale workers, not API replicas: the API path reaches 88 % of the parser-imposed ceiling,
so adding replicas only adds polling load.
