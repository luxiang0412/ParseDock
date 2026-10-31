# RUNBOOK

Operational procedures for a ParseDock deployment: one Coordinator (SQLite + local disk),
N stateless API replicas, M workers. Every alert in
`deploy/observability/alerts.yaml` names the section to read.

## 0. Facts you need before an incident

| What | Where |
| --- | --- |
| Coordinator state | `<data-dir>/coordinator.db` (WAL) + `<data-dir>/{inputs,results,tmp}` |
| Coordinator listener | `--internal-listen`, internal token + mTLS (PR-07) |
| Metrics | `GET /internal/metrics` on the coordinator (internal token) |
| Probes | `/healthz` (liveness), `/readyz` (SQLite, disk watermark, cleanup) |
| SLOs | `docs/SLO.md` |
| Engine ceiling | `--engine-max-rss-mb` (default off), `--max-parse-minutes` |
| Quota | `--quota-concurrent`, `--quota-jobs-per-day`, `--quota-bytes-per-day`, `--quota-storage-mb` |

The single Coordinator is the only stateful component. Restarting it is a normal,
supported operation: jobs are in SQLite, files are committed before the row leaves
`running`, and leases are re-evaluated on start.

## 1. Backup and restore

Two variants, same result layout:

```bash
# Offline (coordinator stopped): simple copy, WAL included.
scripts/backup.sh /data /backups/2026-11-01

# Online (coordinator running): consistent SQLite snapshot + file tree, verified.
scripts/backup_online.py --data-dir /data --out /backups/2026-11-01 --verify
```

If the tool exits non-zero with *"snapshot and pinning disagreed"*, a job changed retention
state (ACK, TTL, cancel) in the milliseconds between the snapshot and the pin. Nothing is
wrong with the data — the tool removes the partial destination, so just re-run the same
command: it takes a fresh snapshot. It never writes a backup it could not protect.

`backup_online.py` prints a JSON report (`jobs_servable`, `files`, `bytes`, `db_seconds`,
`verify.problems`). `--verify` runs `PRAGMA integrity_check` and re-hashes every artifact.

**What is in a backup:** the database and the inputs/results of jobs whose retention
status is `active`. Released and expired results are *not* copied — an ACK is a deletion
request, and a restore must never resurrect one.

Two properties that make the online variant safe on a live coordinator:

1. **The copy is pinned.** While the files are copied, the tool inserts `download_leases`
   for the snapshot's active jobs, which the cleaner already honours. A result ACKed or
   expired *during* the backup therefore cannot be deleted mid-copy.
2. **The destination must be empty.** Reusing a directory would overlay the new job set on
   top of the old tree and silently keep artifacts that are no longer servable; the tool
   refuses instead.

**RPO / RTO.** RPO is your backup interval; the snapshot itself is consistent. RTO was
measured on the PoC host with the restore drill:

```bash
make drill        # tests/integration/pr12_drill.py
# PR12-01 PASS online backup verified in 0.0s (1 servable jobs, 6 files, problems=[])
# PR12-05 PASS RTO 0.1s (restore + start until /readyz)
# PR12-02 PASS active result after restore → 200 (version preserved)
# PR12-04 PASS released job after restore: state=released, result=410
```

Restore:

```bash
systemctl stop parsedock-coordinator        # or: docker compose stop coordinator
scripts/restore.sh /backups/2026-11-01 /data   # refuses a non-empty target
systemctl start parsedock-coordinator
curl -sf localhost:8081/readyz && echo ready
```

`restore.sh` deletes artifacts of any job that is not `active` and re-runs the cleaner on
start, so a stale row can never point at a missing file.

## 2. Coordinator upgrade (zero result loss)

```bash
# 1. Drain uploads: stop the API replicas (they hold no state) or point the ingress at
#    a maintenance page. In-flight uploads are short; jobs already queued keep running.
kubectl scale deploy/parsedock-api --replicas=0     # or: docker compose stop api

# 2. Wait for in-flight work to settle (metrics, not guessing).
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/metrics \
  | grep -E 'parsedock_jobs\{state="running"\}|parsedock_queue_depth'
#    running=0 is ideal; a non-zero queue is fine — the new coordinator picks it up.

# 3. Stop, upgrade, start the coordinator. SQLite migrations run at start.
scripts/backup_online.py --data-dir /data --out /backups/pre-upgrade --verify
systemctl stop parsedock-coordinator && systemctl start parsedock-coordinator

# 4. Bring the API replicas back and verify.
kubectl scale deploy/parsedock-api --replicas=3
curl -sf localhost:8081/readyz && echo ready
```

Rollback is `restore.sh` of the pre-upgrade backup into an empty directory plus the previous
binary/image. Never restore over a live data dir.

## 3. Incident playbooks

Each starts with the metric that fires and ends with the command that proves recovery.

### 3.1 Coordinator down (`ParseDockCoordinatorDown`)

```bash
systemctl status parsedock-coordinator; journalctl -u parsedock-coordinator -n 100
curl -sf localhost:8081/readyz || curl -s localhost:8081/readyz | jq
```
Restart. If `readyz` reports `sqlite: down`, check disk space and file permissions before
restarting; if it reports `storage`, see 3.5. Workers reconnect automatically and requeue
jobs whose lease expired.

### 3.2 Queue stalled (`ParseDockQueueStalled`)

```bash
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/metrics \
  | grep -E 'parsedock_queue_depth|parsedock_oldest_queued_seconds|parsedock_workers_online'
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/diagnostics/workers | jq
```
If workers are online but nothing is claimed, the job needs an engine no worker offers
(`gpu_required` without a GPU worker returns `409` at creation, so this is usually an
`--engines` mismatch after a deploy). Check a worker log for `claim failed` and the engine
probe result at start-up.

### 3.3 No workers online (`ParseDockNoWorkersOnline`)

Registration fails on: wrong `--internal-token`, missing/expired client certificate
(`tls: bad certificate` in the worker log), or a coordinator URL pointing at the public
port. Verify with:

```bash
curl -sS --cacert ca.crt --cert worker.crt --key worker.key \
  -H "X-Internal-Token: $TOKEN" https://coordinator:8081/internal/diagnostics/workers | jq
```

### 3.4 Cleanup failing (`ParseDockCleanupFailing`)

```bash
curl -s localhost:8081/readyz | jq .checks.cleanup
journalctl -u parsedock-coordinator | grep 'cleanup pass' | tail -5
```
Retention cannot advance: expired results stay on disk (data is not lost, quota is).
The usual cause is a read-only or full volume; the job stays in `deleting` and the next pass
retries (PR-06 made finalization atomic for exactly this reason).

### 3.5 Disk low / full (`ParseDockDiskLow`)

```bash
df -h /data; du -sh /data/{inputs,results,tmp}
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/metrics | grep parsedock_disk_free_bytes
```
`/readyz` already fails below the watermark, so new uploads are refused with `507` rather
than filling the disk. Free space by letting retention run (`--result-ttl`), releasing
results (`POST /v1/jobs/{id}/ack`), or increasing the volume. `tmp/` and `work/` leftovers are
swept automatically; `du` there first if the numbers do not add up.

### 3.6 Lease storm (`ParseDockLeaseStorm`)

```bash
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/metrics | grep leases_expired
journalctl -u parsedock-coordinator | grep lease_expired | tail -5
```
Causes, in order of likelihood: worker restarts (each restart abandons its lease),
coordinator pauses longer than `--worker-lease` (GC, disk stall, VM freeze), clock skew
between hosts. Fix the cause; the engine's retry budget (`--max-attempts`, default 3) caps
the damage, and a job that exhausts it fails with `engine_unavailable`/`timeout`.

### 3.7 Engine OOM loop

Symptom: the same job fails with `engine_out_of_memory` on every attempt, or a worker log
shows `SIGKILL` after `engine exceeded --engine-max-rss-mb`.

```bash
journalctl -u parsedock-worker | grep -E 'engine_out_of_memory|out of memory' | tail
```
The RSS ceiling kills the whole process group (PR-04); the job is retried with backoff. If
it persists, the file is genuinely larger than the worker can handle: raise
`--engine-max-rss-mb`/worker memory, or route it to a bigger worker. For 550–770 MiB
expanded XLSX ledgers this is expected until sheet-window extraction lands (RC-2).

### 3.8 Certificate expiry

```bash
openssl x509 -in coordinator.crt -noout -enddate
openssl x509 -in worker.crt -noout -enddate
```
Renew every certificate with the same CA, replace the files, restart the process (certificates
are loaded at start-up). During the change window, keep the old certificate valid until every
replica and worker has restarted. `--tls-insecure` is never an acceptable workaround in a
shipped configuration.

### 3.9 Tenant throttled / 429

```bash
curl -s -H "X-Internal-Token: $TOKEN" localhost:8081/internal/metrics \
  | grep -E 'parsedock_throttled_total'
```
Status polling shares the per-owner budget (RC-5). Point the client at
`GET /v1/jobs/{id}/events` (SSE) or raise `--rate-limit`/`--rate-burst`. A `quota_exceeded`
429 is a plan limit, not a bug: check `--quota-*` and the `Retry-After` header.

## 4. Verification commands

Note: a job that failed permanently keeps `retention_status=active` while its files are
already gone (the coordinator deletes them on failure). Backups record such rows as
metadata-only and verification does not demand their inputs/artifacts; only `queued`,
`running` and `completed` jobs must have files.


```bash
make drill            # online backup + restore drill, RTO printed
make gate             # engine quality/latency gate
bash scripts/ci-local.sh lifecycle   # ACK/TTL/lease/restart/cancel behaviour
bash scripts/ci-local.sh all         # everything CI runs
```

## GPU jobs: reading the evidence

Since PR-D1 a `gpu_required` job is published only when the adapter can prove that
*this task* ran on a GPU. When it cannot, the job fails with the existing
`gpu_unavailable` code and the message names the engine, the evidence and its
source, for example:

```
gpu_required: mineru cannot prove GPU execution (unknown(mineru:no-session-line)):
the daemon initialised no ONNX session during this task (models already loaded),
so the device of this task is unknown
```

Checklist when that happens:

1. `docker exec <worker> nvidia-smi` — the device being visible is necessary, not
   sufficient.
2. Read the engine's own evidence:
   * MinerU: `$MINERU_HOME/logs/doclib.log`.
     * `providers=['CPUExecutionProvider'], gpu=None` plus
       `ONNX device selection failed; using CPU … libvulkan.so.1` ⇒ the image has
       no Vulkan loader (fixed in `parsedock-worker-gpu:0.2.0-d2`).
     * `vkCreateInstance: Found no drivers!` / `Could not get 'vkCreateInstance'
       via 'vk_icdGetInstanceProcAddr'` ⇒ the ICD cannot initialise **inside the
       container** even though it works on the host: an NVIDIA driver/toolkit
       problem, documented in `docs/OCR-PR-D2-REPORT.md`. Do not "fix" it with a
       software Vulkan driver (llvmpipe) — PR-D1 would then report GPU execution
       for CPU work.
     * `providers=['CUDAExecutionProvider'], gpu=0` written **during** the task ⇒
       the job should have been accepted; cross-check the worker log line
       `msg="execution evidence"` for the same job.
   * docling-rs: stderr `… built without that provider … using CPU` means a GPU
     provider was requested and silently refused; it is reported as CPU and
     `gpu_required` fails.
3. Count the outcomes over a window from the worker log (one line per parse):

```
docker logs <worker> 2>&1 | grep '"msg":"execution evidence"' \
  | python3 -c 'import sys,json,collections
c=collections.Counter(json.loads(l)["kind"] for l in sys.stdin if l.startswith("{"))
print(c)'
```

`gpu_preferred` jobs may fall back; their IR still records `cpu` or `unknown`, so
a dashboard must read `routing.actual_device`, never `requested_device`.
