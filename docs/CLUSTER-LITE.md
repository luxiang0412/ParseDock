# CLUSTER-LITE — stateless API + one Coordinator + remote workers

> Scope: horizontal **compute** scaling, not multi-active HA. One Coordinator owns
> SQLite and the files. This is a deliberate P0 constraint.

## 1. Topology

```
 Internet/intranet
        │  HTTPS + bearer token
   ┌────┴─────┬──────────┐
   ▼          ▼          ▼
 api-1      api-2      api-N      stateless, no DB, no files, no sticky sessions
   └────┬─────┴──────────┘
        │ internal network, X-Internal-Token + X-Parsedock-Owner
        ▼
 ┌────────────────────────────┐
 │ Coordinator (single host)  │  SQLite WAL + data/{inputs,results}
 │  /v1/* internal            │  cleaner, leases, fencing
 │  /internal/tasks/*         │
 └─────┬───────────┬──────────┘
       │           │  worker protocol (pull)
       ▼           ▼
  worker-cpu-1  worker-gpu-1     any host, any count
```

Verified on this host: 2 API processes + 1 coordinator + 1 worker, plus a
Docker Compose variant (`deploy/compose.cpu.yaml`, api-1 on :8080, api-2 on :8082,
coordinator unreachable from the host network).

## 2. Why the API is genuinely stateless

* No filesystem access, no SQLite handle, no in-memory job map (code: `internal/api/server.go`).
* It injects identity and reverse-proxies; streaming and SSE pass through unbuffered.
* Test T10: create on replica A, poll + download + ACK on replica B; SSE read from
  replica B; result version identical. No session affinity anywhere.

## 3. Worker protocol (internal, not a public contract)

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/internal/workers/register` | capabilities + heartbeat, returns worker id |
| POST | `/internal/workers/{id}/heartbeat` | keep registration fresh |
| POST | `/internal/tasks/claim` | atomically lease one matching job (`204` if none) |
| GET | `/internal/tasks/{id}/input` | stream input; `X-Lease-Token` required; `X-Content-SHA256` returned |
| POST | `/internal/tasks/{id}/heartbeat` | renew lease + progress (`progress_percent`, `stage`) |
| PUT | `/internal/tasks/{id}/result` | IR JSON; fenced commit |
| POST | `/internal/tasks/{id}/fail` | classified failure (`code`, `message`, `retryable`) |

Registration payload includes `engines[]`, `device_type`, `gpu_model`,
`gpu_memory_bytes`, `cpu_cores`, `memory_bytes`, `max_concurrency`.

### Claim is one SQL statement

```sql
UPDATE jobs SET processing_status='running', started_at=COALESCE(started_at,?), ...
WHERE id = (SELECT id FROM jobs
            WHERE processing_status='queued'
              AND (available_at='' OR available_at<=?)
              AND engine_hint IN (?, 'auto')
              AND max_lifetime_at > ?
              AND requested_device != 'gpu_required'   -- only when worker lacks a GPU
            ORDER BY priority DESC, created_at LIMIT 1)
  AND processing_status='queued'
RETURNING id, format, …;
```

The `RETURNING` result plus the attempt insert happen in the same transaction, so
two workers can never lease the same job.

### Fencing

* Each attempt gets `lease_token` (`lt_…`); every heartbeat / result / fail must present it.
* The result commit is conditional on
  `current_attempt = (attempt with this token) AND state='running' AND lease_until > now`.
* A superseded attempt that finishes late gets `409 lease_lost` and its freshly written
  files are deleted — it cannot overwrite a newer result.
* T15: after the job completed, a late `PUT …/result` and a late heartbeat from the
  old token both returned `409`, and `result_version`/`page_count` were unchanged.

### Lease recovery

`lease_until = now + worker_lease` is renewed every `heartbeat` (default 60 s / 10 s).
The cleaner reaps expired attempts and requeues with exponential backoff
(1s, 2s, 4s … capped at 60 s) up to `max_attempts` (3), then fails the job with
`lease_lost`.

T09 evidence: worker `SIGKILL` mid-task → attempt 1 `expired`, job requeued,
attempt 2 on a new worker `succeeded`, stale tokent rejected. End-to-end 50 s
(5 s lease + backoff), which is the expected cost of the recovery window.

## 4. Failure behaviour

| Failure | Observed behaviour |
| --- | --- |
| API replica dies | other replicas unaffected (no state); clients retry |
| Coordinator restart (SIGKILL) | API returns `502 coordinator_unavailable` until it is back (< 5 s measured); committed jobs survive; pending deletions resume (T08) |
| Worker dies | lease expiry → requeue → retry (T09) |
| Worker stalls (no heartbeat) | same as death, but after `worker_lease` |
| Client cancels a running job | heartbeat returns `409 cancelled`; the worker stops, finalises via an idempotent `/fail`, job stays `cancelled` | `TestCancelledAttemptFinalisation`, PR00-03 |
| Coordinator disk full | uploads `507`, reads/ACK keep working (T14) |
| Network partition worker↔coordinator | heartbeats fail → worker aborts the parse; lease expires; job retried |
| Duplicate claim attempt | impossible (single-statement CAS) |

## 5. File transfer path and its bottleneck

Every byte of every input and result goes through the Coordinator:

```
POST /v1/jobs  → api → coordinator → data/inputs/<job>/input.bin
GET  …/input   → coordinator → worker
PUT  …/result  → worker → coordinator → data/results/<job>/*
GET  …/result  → coordinator → api → client
```

Measured on loopback (see `ENGINE-BENCHMARK.md` §5): the coordinator sustains
~5 000 small-job HTTP round trips and streams ~1.5 GB/s on `/tmp`-class NVMe for
large results, i.e. the parse engine, not the file path, is the limit for the
tested corpus. That will not hold for multi-hundred-MB scans: the upgrade
triggers are:

1. **sustained > 60 % of one NIC** on the coordinator host during result streaming, or
2. **p99 result download > 2× the engine parse time** for the same document, or
3. **> 1 TB/day** of input+output through the coordinator, or
4. worker count where claim/heartbeat writes dominate SQLite (`wal` growth > 1 GB/h).

Mitigations in order (no new infrastructure until evidence demands it):
(a) enable gzip/br for text results, (b) split large artifacts to a shared-filesystem
path with the coordinator storing only manifests, (c) object storage + pre-signed URLs.

## 6. Deployment notes

* Coordinator internal API must listen on a private interface; never publish `8081`.
* **mTLS between replicas, coordinator and workers (PR-07).** `PARSEDOCK_INTERNAL_TOKEN`
  remains the application-level authorisation layer, but transport identity is now
  cryptographic:

  | role | flags |
  | --- | --- |
  | coordinator | `--tls-cert`, `--tls-key`, `--tls-client-ca` (enables `RequireAndVerifyClientCert`) |
  | api replica | `--tls-ca`, `--tls-cert`, `--tls-key`, `--tls-server-name` |
  | worker | same as the api replica |

  Without `--tls-cert/--tls-key` the listener stays plaintext (single-host development
  only). `--tls-insecure` exists for throwaway certificates and is refused by the
  release-gate checklist. TLS 1.2 is the floor; certificates are reloaded only on
  restart, because a coordinator restart is already a documented operation.
  `tests/integration/pr07_tls.py` proves, against real processes: a client without a
  certificate is rejected, a certificate from a foreign CA is rejected, plaintext
  HTTP on the TLS port is rejected, an authorised api replica proxies a job end to
  end, a worker completes a job over mTLS, an api replica *without* a client
  certificate cannot reach the coordinator (502), and the coordinator log contains
  neither document bytes nor secrets.
* SQLite must stay on local disk. WAL over NFS/SMB is unsupported by SQLite and by us.
* Workers need only outbound access to the coordinator; engine egress can be blocked
  (see `--sandbox=unshare-net`, which fails closed when namespaces are unavailable).
* **Worker scratch (PR-00 / F09).** Each attempt gets `<work-dir>/<job>/a<N>`, removed
  on every exit path, plus three safeguards against `SIGKILL`/partition leftovers:
  a startup sweep, a periodic scrub (`--work-scrub-interval`, default 10 min) and a
  maximum age (`--work-max-age`, default 1 h). Attempt directories of tasks this
  worker is currently running are never removed. Unit-tested by
  `TestSweepScratch*`; the scrubber logs what it removes.
* **Identity boundary (PR-00 / F04).** The API gateway strips client-supplied
  `X-Parsedock-Owner` / `X-Internal-Token` before proxying, the public mux has no
  `/internal` route (verified by contract test and PR00-10), and the coordinator
  **refuses to bind a non-loopback internal address without an internal token**
  (`ValidateInternalBinding`). Key rotation is two keys mapped to the same stable
  `owner_id`; jobs stay reachable across the rotation (PR00-10).

## 7. Measured load (single host, cluster-lite topology)

`scripts/cluster-lite-load.sh` starts one coordinator, **two** api replicas and **two**
workers with the deterministic mock engine (fixed 200 ms parse), then drives jobs
through *one* replica with `tests/load/loadgen.py` (create + poll to terminal).

| jobs | concurrency | workers | throughput | p50 | p95 | p99 | failures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 120 | 12 | 2 | 8.84 jobs/s | 1.23 s | 1.53 s | 2.15 s | 0 |
| 200 | 64 | 2 | 8.75 jobs/s | 6.68 s | 7.39 s | 8.23 s | 0 |

Reading: two workers parsing 200 ms each cap the stack at 10 jobs/s; the measured
8.8 jobs/s is 88 % of that ceiling, so **the coordinator/API path is not the
bottleneck at 14 concurrent pollers/submitters per replica** (≈2.5 k requests/s
including status polling). Latency at 64 concurrent submissions is queueing time, not
scheduler overhead. Scaling therefore means adding workers, not replicas — consistent
with §5. `runtime/cluster-lite-load/` keeps the raw reports (git-ignored).

## 8. What the evidence does *not* cover

* Multi-host testing was limited to one machine (two API processes, two workers,
  loopback). Cross-host latency, MTU and partition behaviour are designed for but not
  measured; mTLS is verified, but certificate distribution/rotation for a fleet is
  operational work outside this PR.
* No soak test beyond the acceptance runs (~15 min) and the benchmark (~30 min).
* No Coordinator failover automation: the orchestrator must restart the single
  Coordinator; state recovery is verified, automatic leader election is out of scope.
