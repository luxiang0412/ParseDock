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

## 2a. Single-host stack with the Playground (4 containers)

`deploy/host/docker-compose.yml` is the smallest deployment that serves both the
API and the web UI from one host. It publishes exactly two ports and nothing
else:

```bash
cd deploy/host && cp .env.example .env      # then fill in the three secrets
openssl rand -hex 24                        # PARSEDOCK_INTERNAL_TOKEN, API key, PLAYGROUND_KEY
docker compose up -d
```

| Service | Image | Published | State |
| --- | --- | --- | --- |
| `coordinator` | `parsedock:0.1.0` | — (internal net) | `/data` volume: SQLite + inputs + results |
| `api` | `parsedock:0.1.0` | `8080` | stateless; add replicas freely |
| `worker-cpu` | `parsedock-worker-cpu:0.1.0` | — (internal net) | tmpfs scratch, 2 parse slots |
| `playground` | `parsedock-playground:0.1.0` | `8090` | static app + same-origin `/v1` proxy |

Both ports are overridable with `API_PORT` / `PLAYGROUND_PORT`.
`PARSEDOCK_XBERG_OCR_LANG` must match the corpus (`zh` for Chinese scans); a
wrong value silently degrades OCR instead of failing the job.

Verified on a second host (Docker 27.1.2, Compose v2.29.1, x86-64): API
`/healthz` and `/readyz` 200, unauthenticated `401`, `POST /v1/jobs` → `completed`
via `xberg` in 14 ms for a 3-page PDF, Markdown download 200, ACK 202 then 410,
the proxy returning 404 for `/internal/*` and 403 without the CSRF header, and
the Playground E2E suite (10 passed, 1 skipped) against that deployment.
See `deploy/host/README.md` for prerequisites, image transfer, verification and
operations.

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

The GPU image (1.0 GB, verified end to end by `make gpu-check`) keeps MinerU's local
daemon on a **persistent volume**, not tmpfs: the first run downloads several GB of model
weights into `HF_HOME`, and a restart should not pay that cost again.

```bash
docker volume create parsedock-mineru-home          # model cache survives restarts
docker run -d --name worker-gpu --device nvidia.com/gpu=all --network host \
  -v parsedock-mineru-home:/work \
  -e PARSEDOCK_INTERNAL_TOKEN="$TOKEN" \
  parsedock-worker-gpu:0.1.0 --role worker \
  --coordinator-url http://10.0.0.10:8081 --engines mineru --device-type gpu \
  --work-dir /work/scratch
```

* The container starts as root only long enough to `chown` the mounted volume, then
  re-executes itself as uid 10001 via `setpriv` before any parser runs.
* `HOME`, `HF_HOME` and `XDG_CACHE_HOME` point inside the volume; otherwise MinerU tries
  to write `/root/.cache/huggingface/token` and fails.
* Scratch is always a **subdirectory** (`/work/scratch`), never the volume root: the
  worker's scrubber deletes attempt directories older than an hour, and pointing
  `--work-dir` at `/work` would delete the multi-GB model cache with them. (The scrubber
  now only touches directories named like its own `<job_id>/a<attempt>` scratch, which
  protects a misconfigured root, but scratch still belongs in a subdirectory.) The entrypoint
  appends `--work-dir /work/scratch` when the flag is absent, and
  `deploy/compose.gpu.yaml` sets it explicitly.
* Telemetry is disabled at build time and again at start-up. Only the `flash` tier is
  reachable; block egress to `mineru.net` by policy and run parsers with
  `--sandbox=unshare-net` where namespaces are available.
* Give every GPU worker its own MinerU daemon (its own `MINERU_HOME`). The per-task GPU
  evidence is read from the daemon log written during the task, and those lines carry no
  task id: a daemon shared between workers would let one task's session lines count as
  another's.
* The worker reports the free VRAM of the card it will use (first entry of
  `CUDA_VISIBLE_DEVICES`, else device 0) when it registers: after every task and, while
  idle, at most once per `--heartbeat` interval (default 10 s) — pin `CUDA_VISIBLE_DEVICES`
  if the host has several cards. A GPU worker runs one task at a time
  (`--worker-concurrency` is capped at 1) because per-card VRAM cannot be partitioned
  between slots; scale with more workers. Set
  `--engine-min-vram-mb mineru:8192` on the coordinator so `gpu_required` is refused with
  `409 gpu_unavailable` (naming the requirement) instead of queueing on a full card.

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
| `--max-pages` | `PARSEDOCK_MAX_PAGES` | `2000` | PDF page cap; the pre-flight count is a heuristic (head/tail scan), so this bounds work rather than guaranteeing an exact cut. A 553-page audit annexe in the real corpus needed this. |
| (limits) | – | 512 MiB expanded, 200:1 ratio | OOXML expansion cap; the parsers themselves cap lower (xberg 500 MiB, docling-rs 10M cells per sheet) |
| `--min-free-gb` | `PARSEDOCK_MIN_FREE_GB` | `2` | disk watermark |
| `--max-attempts` | `PARSEDOCK_MAX_ATTEMPTS` | `3` | retries |
| `--rate-limit` / `--rate-burst` | `PARSEDOCK_RATE_LIMIT(_BURST)` | `20` / `40` | per-owner, **all** requests: status polling counts against it (a batch client polling at 5 rps/thread exhausted the default 20 rps and received `429`; use SSE or raise the limit for batch tenants) |
| `--api-keys-file` | `PARSEDOCK_API_KEYS_FILE` | – | one `<secret\|sha256:hash>:<owner>[:scopes]` per line; **preferred over `--api-keys`**, because argv is readable by other local users |
| `--api-keys` | `PARSEDOCK_API_KEYS` | – | comma-separated same format; hashed at startup, kept for development |
| `--quota-concurrent` | `PARSEDOCK_QUOTA_CONCURRENT` | `0` (off) | queued+running jobs per owner |
| `--quota-jobs-per-day` | `PARSEDOCK_QUOTA_JOBS_PER_DAY` | `0` (off) | jobs per owner per rolling 24 h |
| `--quota-bytes-per-day` | `PARSEDOCK_QUOTA_BYTES_PER_DAY` | `0` (off) | input bytes per owner per rolling 24 h |
| `--quota-storage-mb` | `PARSEDOCK_QUOTA_STORAGE_MB` | `0` (off) | live result bytes per owner; accounting is released when a result is released/expires |
| `--max-expanded-mb` | `PARSEDOCK_MAX_EXPANDED_MB` | `512` | OOXML expanded-size cap; the parsers cap lower (xberg 500 MiB, docling-rs 10 M cells/sheet) |
| `--sandbox` | `PARSEDOCK_SANDBOX` | `` (off) | `unshare-net` |
| `--xberg-ocr-backend` (worker image) | `PARSEDOCK_XBERG_OCR_BACKEND` | ` ` (unset → tesseract) | OCR backend for xberg: leave unset for today's behaviour, or set `paddle-ocr` (PP-OCRv6 small, in-process, no extra dependency) on a Chinese corpus. Measured on 300 dpi scans: `paddle-ocr` reads Chinese at CER 0.000 and English at 0.000, where tesseract is 0.075/0.453 with the wrong language, at ~3x the latency (1.25 s vs 0.41 s per 2-page scan) and ~3.4x the memory. `eng`, `en`, `chi_sim`, `zh` and `ch` all map to the right recognition family; an unknown name fails at startup rather than on the first document. `--ocr-backend-options` is deliberately **not** exposed: xberg 1.3.10 silently ignores unknown option keys, so it cannot be verified. |
| `--xberg-ocr-lang` | `PARSEDOCK_XBERG_OCR_LANG` | `eng` | OCR language passed to the engine. **Match the corpus**: on the scanned Chinese fixture `eng` produced garbage while `zh` reproduced the page exactly ("采购订单 / 供应商：华北物流有限公司 / 金额：人民币128,400元"), and `zh` degrades English scans ("Supler. Nwn Lic"). A combined value (`chi_sim+eng`) is rejected by xberg 1.3.10. Run one worker per language, or point the whole deployment at the one that dominates. A wrong language is only *partly* detected: a Chinese page read with `eng` loses OCR confidence (0.38–0.51) and `mode=auto` retries it once on the accurate engine, but an English page read with `chi_sim` stays confident (0.87) and is not retried (measured 3 % CER loss). With `--xberg-ocr-backend paddle-ocr` the language choice stops mattering for Chinese/English. |
| `HF_HUB_CACHE` (worker image) | – | `/opt/xberg-models` | where the worker image seeds xberg's PaddleOCR/Sceptre models at build time (652 MiB). `paddle-ocr` is the only backend that needs model files beyond the tesseract tessdata; without the seed it would download them at run time, which fails on an internal worker network. |
| `--xberg-timeout` / `--docling-rs-timeout` / `--mineru-timeout` | `PARSEDOCK_*_TIMEOUT` | `10m` / `15m` / `30m` | per-document engine ceiling, capped by `--max-parse-minutes` and the job deadline |
| `--engine-max-rss-mb` | `PARSEDOCK_ENGINE_MAX_RSS_MB` | `0` (off) | kill an engine process above this RSS and fail the task with retryable `engine_out_of_memory`; cgroups remain the deployment-level limit |
| `--device-type` | `PARSEDOCK_DEVICE_TYPE` | `auto` | advertised device (`cpu`/`gpu`/`cuda`/`mps`); a GPU worker is capped to one task at a time. The label is an operator statement: since PR-D1 a `gpu_required` job additionally needs **per-task proof** that the GPU was used, otherwise it fails with `gpu_unavailable` (see `docs/RUNBOOK.md` → "GPU jobs: reading the evidence"). Registration reports `gpu_inference_verified: false`, so a visible device is never presented as a verified capability. |
| `--worker-id` | `PARSEDOCK_WORKER_ID` | generated | stable worker identity across restarts |
| `--worker-concurrency` | `PARSEDOCK_WORKER_CONCURRENCY` | `1` | parallel slots per worker; ignored (capped to 1) for GPU device types |
| `--work-dir` | `PARSEDOCK_WORK_DIR` | `./runtime/work` | worker scratch; on the GPU image keep it **below** the model-cache volume (`/work/scratch`) |
| `--engine-min-vram-mb` | `PARSEDOCK_ENGINE_MIN_VRAM_MB` | – | `engine:MiB` list; `gpu_required` is refused (`409 gpu_unavailable`) unless a live GPU worker has that much free on the card the parser will use |
| `--tls-cert` / `--tls-key` | `PARSEDOCK_TLS_CERT` / `_KEY` | – | coordinator listener certificate; also used as the client certificate on api/worker |
| `--tls-client-ca` | `PARSEDOCK_TLS_CLIENT_CA` | – | enables mutual TLS on the coordinator; **startup fails without `--tls-cert/--tls-key`** |
| `--tls-ca` | `PARSEDOCK_TLS_CA` | – | CA that verifies the coordinator (api/worker) |
| `--tls-server-name` | `PARSEDOCK_TLS_SERVER_NAME` | – | required when dialling an IP literal |
| `--tls-insecure` | `PARSEDOCK_TLS_INSECURE` | off | skip verification — development only, refused by the release checklist |
| `--escalate-scans` | `PARSEDOCK_ESCALATE_SCANS` | `1` | retry a CJK scan once with the accurate engine when the fast engine reports low OCR confidence (only `mode=auto`, only when the retry yields at least as much text) |
| engine paths | `PARSEDOCK_XBERG_BIN`, `PARSEDOCK_DOCLING_RS_BIN`, … | – | adapters |

`docparse --check` prints host facts, the effective config and the routing table as
JSON — useful as a deployment smoke test and for support tickets.

### Key rotation

Keys are hashed at startup; two entries mapping to the same owner rotate without
touching jobs:

```
# /run/secrets/parsedock-api-keys
sha256:<hex of old key>:acme:jobs:write+jobs:read+jobs:ack
sha256:<hex of new key>:acme:jobs:write+jobs:read+jobs:ack
```

`printf '%s' "$TOKEN" | sha256sum` produces the hash. Remove the old line and
reload the API replicas; jobs keep their owner id, so nothing is lost. A key that
is no longer in the file is rejected with `401` (revocation).

### Audit shipping

Coordinator log lines with `audit=true` are the audit trail; forward them to your
log pipeline separately from application logs (they contain identities and sizes,
never document text). Example event schema: `docs/ARCHITECTURE.md` §6.

## 4a. Error codes

Every error response is `{"error":{"code","message"}}`, and a failed job carries the
same `code` in `error_code`. The full table (HTTP status, whether a client should retry,
meaning, which component raises it) is [`ERROR-CODES.md`](ERROR-CODES.md); alerting and
client retry policies should key on `code`, never on `message`.

## 5. Probes

| Endpoint | Role | Meaning |
| --- | --- | --- |
| `GET /healthz` (api) | liveness | process is serving |
| `GET /readyz` (api) | readiness | proxies the coordinator's readiness |
| `GET /readyz` (coordinator) | readiness | SQLite ping, free bytes vs watermark, cleaner not stalled (`503` when stalled) |

The images do not ship an HTTP client, so the compose file intentionally has no
container-level `healthcheck`. Point your orchestrator's HTTP probe at these
endpoints (they are reachable on the internal network for the coordinator and on
the published port for the API replicas).

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

## 6a. Capacity planning (measured)

Measured on the 958-file real corpus (`docs/REAL-CORPUS-TEST.md`) with xberg +
docling-rs, 4 workers on one 24-core host:

| Metric | Value |
| --- | --- |
| input bytes | 2.3 GiB of documents (PDF-heavy, 58–74 MiB ledgers included) |
| wall time | 419 s at client c=4 (2.3 files/s) / **267 s at c=16 (3.6 files/s)**; engine p50 265 ms / p95 1.36 s for PDFs |
| result bytes | ≈ 1.9× the input text volume for JSON + Markdown + Text (3 artifacts per job) |
| disk per job | input + 3 artifacts; released/expired jobs are removed at TTL or ACK |
| peak RSS | xberg ≈ 40 MB, docling-rs ≈ 0.9–1.2 GB per concurrent parse |

Sizing rule of thumb: `data-dir` ≈ 2.5 × (bytes/day) × (TTL in days) + SQLite
(≈ 2 KB/job before tombstone purge). Set `--quota-storage-mb` per tenant so one
customer cannot fill the volume, and `--max-upload-mb` to the tenant's largest real
document (the corpus needed 128 MiB; the default is 64).

## 6b. Measured limits on a real corpus

A 958-file real corpus (554 PDF, 294 XLSX, 71 XLSM, …) parsed end-to-end with
`xberg` + `docling-rs` at a 96.9 % completion rate. The remaining failures are
limits, not bugs, and they are reported with stable codes:

| Cause | Code | Corpus count | Action |
| --- | --- | --- | --- |
| expanded OOXML > parser limit (xberg 500 MiB, docling-rs 10M cells) | `expanded_too_large` (413) | 14 (58–74 MiB SAP ledgers) | raise/prune at the source, or add per-sheet window extraction (PR-06) |
| encrypted PDF | `encrypted_file` | 8 | out of scope (no password support) |
| > `--max-pages` | `page_limit_exceeded` | 0 (after raising the default to 2000) | raise `--max-pages` if the tenant needs it |
| corrupt/zero-padded PDF, empty XLSX | `unsupported_type` / `malformed_file` | 5 | rejected before an engine runs |

`tests/integration/batch_parse.py` reproduces the run (redacted by default:
hashed file ids, no document text); full report in `docs/REAL-CORPUS-TEST.md`.

## 6c. Mutual TLS (cluster-lite)

The internal token stays, but mTLS is what proves *which host* is calling. Generate one
CA per deployment; the coordinator is the server, api replicas and workers are clients.

```bash
# CA (keep the key offline after issuing certificates)
openssl req -x509 -newkey rsa:4096 -nodes -days 1825 -keyout ca.key -out ca.crt \
  -subj "/CN=parsedock-internal-ca"

# coordinator (server): SAN must cover the address the clients dial
openssl req -newkey rsa:2048 -nodes -keyout coordinator.key -out coordinator.csr \
  -subj "/CN=coordinator"
printf "subjectAltName=DNS:coordinator.internal,IP:10.0.0.10\n" > server.ext
openssl x509 -req -in coordinator.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -days 730 -out coordinator.crt -extfile server.ext

# client certificate (repeat per api replica / worker)
for name in api-1 worker-1 worker-2; do
  openssl req -newkey rsa:2048 -nodes -keyout $name.key -out $name.csr -subj "/CN=$name"
  printf "" > client.ext
  openssl x509 -req -in $name.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -days 730 -out $name.crt -extfile client.ext
done
```

```bash
# coordinator
docparse --role coordinator --internal-listen 0.0.0.0:8081 \
  --tls-cert coordinator.crt --tls-key coordinator.key --tls-client-ca ca.crt

# each api replica
docparse --role api --listen 0.0.0.0:8080 \
  --coordinator-url https://coordinator.internal:8081 --tls-ca ca.crt \
  --tls-cert api-1.crt --tls-key api-1.key --tls-server-name coordinator.internal

# each worker
docparse --role worker --coordinator-url https://coordinator.internal:8081 \
  --tls-ca ca.crt --tls-cert worker-1.crt --tls-key worker-1.key \
  --tls-server-name coordinator.internal
```

Notes:

* `--tls-client-ca` is what makes it mutual. Without it the coordinator accepts any
  client, which is only appropriate on a single development host.
* `--tls-server-name` is required when dialling an IP literal, because Go verifies the
  certificate against the dialled name.
* `--tls-insecure` skips verification and exists only for throwaway certificates; the
  release checklist refuses it in a shipped configuration.
* Certificates are loaded at start-up; rotation means replacing the file and restarting
  the process (a coordinator restart is already the documented recovery operation).
* `tests/integration/pr07_tls.py` is the executable version of this section.

## 6d. Backups and drill

```bash
# Online, from a running coordinator: consistent snapshot + verified file copy.
scripts/backup_online.py --data-dir /data --out /backups/$(date +%F) --verify
# Restore into an empty directory (the script refuses a non-empty target).
scripts/restore.sh /backups/2026-11-01 /data
make drill            # the same round trip, measured, on this host
```

Only jobs with `retention_status=active` are copied: a released result must never come back
from a backup. See `docs/RUNBOOK.md` §1 for the drill output and RTO/RPO definitions.

## 6e. Observability

Scrape `/internal/metrics` on the coordinator's **internal** listener with the internal
token (mTLS when enabled):

```yaml
# prometheus.yml
scrape_configs:
  - job_name: parsedock-coordinator
    metrics_path: /internal/metrics
    static_configs: [{targets: ["10.0.0.10:8081"]}]
    # headers: {X-Internal-Token: "..."} via the file_sd/http_headers or a proxy
rule_files: [/etc/prometheus/parsedock-alerts.yaml]
```

* `deploy/observability/dashboard.json` — Grafana overview (queue, latency by engine,
  workers, throttling, disk, cleanup, retention).
* `deploy/observability/alerts.yaml` — queue stall, no workers, cleanup failing, disk low,
  lease storm, throttle spike, coordinator down; each annotation names the runbook action.
* Metric names, SLO targets and the result-loss budget live in `docs/SLO.md`.
* API replicas expose no metrics endpoint of their own: they are stateless and their only
  counters (`parsedock_throttled_total`) are aggregated into the same family, so scrape the
  coordinator and treat replicas as disposable.

## 7. Known deployment gaps

* `--internal-token` is a shared static secret; mTLS (PR-07) now provides transport
  identity on top of it (see §6c). The coordinator refuses to bind a non-loopback
  internal address without the token, and certificate rotation still needs a restart.
* No automatic Coordinator failover; the orchestrator must restart the single replica.
* Public TLS termination is still the ingress/load balancer's job; ParseDock's own TLS
  covers the **internal** hop only.
* No GPU image built in Phase 0 (recipe and acceptance steps in `GPU-REPORT.md` §4).
* `apache/tika:latest-full` is a moving tag in the PoC; pin the digest in production.
* **Distribution gate.** `make license-audit` writes `build/sbom/<image>.json` and
  fails when an image contains a GPL library. `parsedock-worker-cpu` currently fails
  because the xberg release tarball bundles `libx265`; external distribution is
  BLOCKED until licence item L1 is signed off (`docs/LICENSE-REVIEW.md`).
