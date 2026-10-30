# DEPLOYMENT — CPU and optional GPU

## 1. Artefacts

| Artefact | What it is | Size |
| --- | --- | --- |
| `parsedock:0.1.0` | control plane: coordinator / api / worker (native+mock engines only) | 94.8 MB, image id `7d297f009e9f` |
| `parsedock-worker-cpu:0.1.0` | adds xberg 1.3.10, docling-rs 2.53.3 + 531 MiB ONNX models, tesseract 5.5 | 1.19 GB, image id `b83af36161c1` |
| `parsedock-worker-gpu:0.1.0` | optional CUDA worker with MinerU 4.1.2 flash (Dockerfile provided, **not built in Phase 0**) | several GB |
| `deploy/compose.cpu.yaml` | coordinator + 2 API replicas + CPU worker | – |
| `deploy/compose.gpu.yaml` | GPU worker profile (adds to the CPU stack) | – |
| `deploy/compose.cluster-lite.yaml` | coordinator/api here, worker on another host | – |
| `deploy/.env.example` | required secrets/ports | – |

Images run as uid/gid 10001, no shell in the coordinator image beyond `debian-slim`,
`no-new-privileges`, read-only scratch except `/work` (tmpfs) and `/data` (volume).

## 2. CPU stack (verified end-to-end)

```bash
export PARSEDOCK_INTERNAL_TOKEN=$(openssl rand -hex 24)
export PARSEDOCK_API_KEYS="$(openssl rand -hex 24):acme"
cp deploy/.env.example deploy/.env   # or export the two variables
docker compose -f deploy/compose.cpu.yaml --env-file deploy/.env up -d
curl -H "Authorization: Bearer $KEY" -F file=@doc.pdf http://localhost:8080/v1/jobs
```

What was verified on this host (2026-10-30):

| Check | Result |
| --- | --- |
| 3 containers + coordinator healthy | `docker compose ps` → all running |
| Two API replicas reachable (:8080, :8082) | ✅ |
| Upload via api-1, poll/download via api-2 | ✅ same `result_version` |
| `mode=fast` → xberg | ✅ 3-page PDF, engine `xberg` |
| `mode=accurate` → docling-rs | ✅ 2-page Chinese PDF, engine `docling-rs` |
| ACK on api-2 → 410 on api-1 → `released` | ✅ files removed |
| Non-root worker, tmpfs scratch | ✅ |

The coordinator is **not** published on the host: compose puts it on an
`internal: true` Docker network. Only the two API ports are exposed.

## 3. GPU stack (optional)

```bash
# Host prerequisites
nvidia-smi                        # driver present
docker run --rm --device nvidia.com/gpu=all nvidia/cuda:13.2.0-base-ubuntu24.04 nvidia-smi

# Build and run the GPU worker profile
docker build -f docker/Dockerfile.worker-gpu -t parsedock-worker-gpu:0.1.0 .
docker compose -f deploy/compose.cpu.yaml -f deploy/compose.gpu.yaml \
               --profile gpu --env-file deploy/.env up -d
```

The GPU image's entrypoint starts MinerU's local daemon (`mineru server start`) with
`MINERU_HOME` on tmpfs, disables telemetry, waits for the daemon, then execs the
worker. Only the `flash` tier is reachable; egress to `mineru.net` must be blocked by
policy.

Without a GPU, nothing in the CPU stack changes: the GPU profile is additive and the
coordinator only routes `gpu_required` jobs to registered GPU workers (T12 otherwise).

## 4. Configuration reference

Flags map 1:1 to environment variables (`PARSEDOCK_*`). The important ones:

| Flag | Env | Default | Meaning |
| --- | --- | --- | --- |
| `--role` | `PARSEDOCK_ROLE` | `standalone` | role |
| `--listen` | `PARSEDOCK_LISTEN` | `127.0.0.1:8080` | public API |
| `--internal-listen` | `PARSEDOCK_INTERNAL_LISTEN` | `127.0.0.1:8081` | coordinator |
| `--coordinator-url` | `PARSEDOCK_COORDINATOR_URL` | `http://127.0.0.1:8081` | api/worker target |
| `--data-dir` | `PARSEDOCK_DATA_DIR` | `./runtime/data` | SQLite + files (coordinator only) |
| `--engines` | `PARSEDOCK_ENGINES` | `native` | enabled engines |
| `--result-ttl` | `PARSEDOCK_RESULT_TTL` | `1h` | default result retention |
| `--max-result-ttl` | `PARSEDOCK_MAX_RESULT_TTL` | `24h` | client TTL ceiling |
| `--max-job-lifetime` | `PARSEDOCK_MAX_JOB_LIFETIME` | `24h` | absolute job lifetime |
| `--cleanup-interval` | `PARSEDOCK_CLEANUP_INTERVAL` | `60s` | cleaner period |
| `--download-lease` | `PARSEDOCK_DOWNLOAD_LEASE` | `60s` | download lease |
| `--worker-lease` | `PARSEDOCK_WORKER_LEASE` | `60s` | worker lease |
| `--work-max-age` | `PARSEDOCK_WORK_MAX_AGE` | `1h` | remove crashed attempt dirs older than this |
| `--work-scrub-interval` | `PARSEDOCK_WORK_SCRUB_INTERVAL` | `10m` | periodic worker scratch sweep |
| `--heartbeat` | `PARSEDOCK_HEARTBEAT` | `10s` | worker heartbeat |
| `--tombstone-ttl` | `PARSEDOCK_TOMBSTONE_TTL` | `10m` | 410 window |
| `--max-upload-mb` | `PARSEDOCK_MAX_UPLOAD_MB` | `64` | upload cap |
| `--max-pages` | `PARSEDOCK_MAX_PAGES` | `500` | PDF page cap |
| `--min-free-gb` | `PARSEDOCK_MIN_FREE_GB` | `2` | disk watermark |
| `--max-attempts` | `PARSEDOCK_MAX_ATTEMPTS` | `3` | retries |
| `--rate-limit` / `--rate-burst` | `PARSEDOCK_RATE_LIMIT(_BURST)` | `20` / `40` | per-owner |
| `--sandbox` | `PARSEDOCK_SANDBOX` | `` (off) | `unshare-net` |
| engine paths | `PARSEDOCK_XBERG_BIN`, `PARSEDOCK_DOCLING_RS_BIN`, … | – | adapters |

`docparse --check` prints host facts, the effective config and the routing table as
JSON — useful as a deployment smoke test and for support tickets.

## 5. Probes

| Endpoint | Role | Meaning |
| --- | --- | --- |
| `GET /healthz` (api) | liveness | process is serving |
| `GET /readyz` (api) | readiness | proxies the coordinator's readiness |
| `GET /readyz` (coordinator) | readiness | SQLite ping, free bytes vs watermark, cleaner not stalled (`503` when stalled) |

`/readyz` deliberately reports `accepting_uploads=false` when below the watermark while
remaining `ready` for reads/ACKs, so an orchestrator does not kill the process that is
supposed to reclaim space.

## 6. Backup, restore, retention

PR-00 added offline backup/restore scripts and a test:
`scripts/backup.sh <data-dir> <backup-dir>`, `scripts/restore.sh <backup> <data-dir>`,
`python tests/integration/backup_restore.py`. The backup contains the SQLite file
and **only artifacts of `retention_status=active` jobs** — a released or expired
result must not come back from a backup, because ACK is a deletion request. The
restore script purges non-active artifact directories and the cleaner finishes any
pending retention work on start; the test asserts a released job returns `410`
after a restore while an active job still downloads.

* Back up `data/coordinator.db*` **and** the `inputs/`+`results/` trees together;
  a DB without files yields `410` results, files without a DB are orphaned and swept.
* The cleanest backup point is a stopped coordinator, or `sqlite3 coordinator.db ".backup …"`
  (online, WAL-safe) followed by `rsync` of the two file trees.
* `released`/`expired` rows are tombstones (10 min); after that the job id is gone
  (404) on purpose.
* Volume growth is bounded by `result_ttl` x ingress rate; monitor `data/` usage and
  `readyz.checks.free_bytes`.

## 7. Known deployment gaps

* `--internal-token` is a shared static secret (mTLS is P1). The coordinator refuses
  to bind a non-loopback internal address without it.
* No automatic Coordinator failover; the orchestrator must restart the single replica.
* No TLS termination inside ParseDock; put it behind the ingress/load balancer.
* No GPU image built in Phase 0 (recipe and acceptance steps in `GPU-REPORT.md` §4).
* `apache/tika:latest-full` is a moving tag in the PoC; pin the digest in production.
* **Distribution gate.** `make license-audit` writes `build/sbom/<image>.json` and
  fails when an image contains a GPL library. `parsedock-worker-cpu` currently fails
  because the xberg release tarball bundles `libx265`; external distribution is
  BLOCKED until licence item L1 is signed off (`docs/LICENSE-REVIEW.md`).
