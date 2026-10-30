# IMPLEMENTATION PLAN — Phase 1 (post-Phase-0)

> **Phase 1 status (2026-11-01): PR-01 … PR-12 are merged.** `docs/PHASE1-SUMMARY.md`
> has the one-page status, the verified evidence and the three open items that are
> blocked on something other than code (L1 `libx265` licence, a second host, branch
> protection on a free private repo). Every PR below carries its own **Status:** line
> with what was verified and what was deliberately left out.
>
> **PR-00 (Phase 1 readiness) is complete**: see `docs/PHASE1-READINESS.md` and
> `docs/PR00-ISSUE-MATRIX.md`. Landed here: scored decisions generated from data,
> text-format routing, ACK/Cancel/Heartbeat/lease semantics, public/internal
> OpenAPI split, per-representation ETag, any-order multipart, SSE scope, IR
> provenance fixes, worker scratch reclamation, internal binding guard,
> backup/restore and the licence gate. PR-01 may start; PR-02…PR-12 are unchanged
> in scope except where noted below.

Each slice below is an independently mergeable PR with its own contract, tests and
definition of done. Ordering is dependency-driven; the first four are the path to a
usable v0.1 service.

---

## PR-01 — CI baseline and reproducible builds

**Why.** Phase 0 is reproducible by hand; Phase 1 must be reproducible by a machine.

**Scope**
* GitHub Actions (or Jenkins) jobs: `go vet`, `go test ./...`, `go build`,
  OpenAPI validation, `make sdk`, lifecycle acceptance (mock engine) on every PR,
  real-engine acceptance nightly.
* Pin Go toolchain in `go.mod` (currently `go 1.24.4`) and add `make verify-pins`
  that fails if `go.sum`/image digests change without a review label.

**Tests.** CI must fail if: acceptance has a FAIL, spec validation reports issues,
generated SDK differs from the committed one.

**DoD.** A green PR pipeline runs in < 8 min (mock profile) and produces the raw
acceptance JSON as an artifact.

**Status: DONE (PR-01).** Four workflows (`ci`, `engine-changes`, `nightly`,
`release-gate`), all logic in `scripts/ci-local.sh`, pinned inputs in
`images.lock.json` enforced by `scripts/verify_pins.py`, reproducible binary proven
by `scripts/repro-check.sh`, SDK drift checked, licence audit split into control
plane PASS / worker BLOCK. Measured on GitHub-hosted runners: 3 m 53 s – 3 m 58 s
per PR run; nightly 1 m 56 s. Details and residual risks: `docs/PR01-REPORT.md`.
Residual (out of PR-01 scope): GPU runner (PR-08), two-host cluster test (PR-07),
registry publish step (PR-08/PR-12) which must depend on `release-gate`.

---

## PR-02 — Security hardening of the public edge

**Scope**
* API keys: store only `sha256(key)` in config, support rotation with two active
  keys, per-key scopes (`jobs:write`, `jobs:read`, `jobs:ack`).
* Per-owner quotas: concurrent jobs, bytes/day, jobs/day (not just RPS).
* Request-id propagation into every log line and into job events.
* Harden `Content-Disposition` and header injection paths; add `X-Content-Type-Options`.
* Structured audit log (JSONL) for create/ack/cancel with owner, job, byte counts —
  never document text.

**Tests.** Unit tests for key hashing/rotation; integration: revoked key → 401,
quota → 429 with `Retry-After`, audit line present for each lifecycle transition.

**DoD.** No plaintext tokens on disk; audit schema documented in `docs/ARCHITECTURE.md`.

**Status: DONE (PR-02).** SHA-256 key store with per-key scopes and rotation
(`--api-keys-file`), `403 insufficient_scope`, per-owner quotas (concurrent /
jobs per day / bytes per day) enforced by the coordinator with `429`+`Retry-After`,
structured audit lines (`audit=true`, no content), `X-Request-Id` validation and
`X-Content-Type-Options: nosniff` on every response including proxied streams.
Tests: `internal/api/keys_test.go`, coordinator quota/audit tests,
`tests/integration/pr02_contract.py` (6 checks).

---

## PR-03 — Cancel and timeout semantics that actually stop work

**Current state.** Cancel flips state; a worker notices at its next heartbeat (≤ 10 s)
and the engine subprocess is killed by context cancellation.

**Scope**
* Cooperative cancel: heartbeat returns `409 cancelled` → worker kills the engine
  process group (already implemented) *and* reports `POST /fail` with `cancelled`
  so the attempt row closes deterministically.
* `max_job_lifetime` enforcement inside the engine deadline (already) plus a
  per-engine wall-clock ceiling (`--max-parse-minutes`) that survives coordinator restarts.
* Add `cancel_requested_at` to make cancellation observable before it takes effect.

**Tests.** Chaos: cancel during a 30 s mock parse → status `cancelled` within 2 heartbeats,
attempt row terminal, no artifact published, input deleted by retention.

**DoD.** T-cancel added to the acceptance matrix with a hard time budget.

**Status: DONE (PR-03).** `cancel_requested_at` persisted and exposed, worker
discards a result produced after cancellation, per-engine timeouts configurable and
capped by the job deadline, T17 asserts the worker stops within one heartbeat
(< 12 s with a 1 s interval) and leaves no artifacts. Codex review findings on quota
admission (PR-02) were fixed in the same cycle.

---

## Real-corpus backlog (from `docs/REAL-CORPUS-TEST.md`, 958 files)

These are requirements that only a real corpus exposed. They are distributed into
the slices below; nothing here is a bug that is still open (the three defects found
in that run were fixed in PR #3).

| # | Requirement | Evidence | Belongs to |
| --- | --- | --- | --- |
| RC-1 | Legacy OLE2 Office input (`.doc`, `.xls`, `.ppt`) | 12 `.doc` (72 MB) + 2 `.xls` rejected as `unsupported_type`; both engines read `.xls`, xberg/docling list `.doc` | **PR-04** |
| RC-2 | Parser limits must be configurable and honest | 14 XLSX (58–74 MiB, 550–770 MiB expanded) exceed xberg's 500 MiB / docling's 10 M cells; now `expanded_too_large` but not rescuable | **PR-06** |
| RC-3 | Scan-aware routing | 332/958 files (36 %) are scans; CJK scans need the PP-OCR path, Latin scans are fine with xberg | **PR-04** |
| RC-4 | Upload cap vs real documents | 9 files are 58–74 MiB; the default 64 MiB cap rejects them | **PR-06** |
| RC-5 | Rate limits must account for status polling | a batch client polling at 5 rps/thread exhausted the default 20 rps (429 on 36 files) | **PR-09** |
| RC-6 | Encrypted PDFs are 8/554 of real documents | explicit `encrypted_file` today; product decision on password support | PR-06 decision |

---

## PR-04 — Engine productionisation: xberg (fast) + docling-rs (accurate)

**Scope**
* Pin artefacts by digest: xberg tarball sha256, docling-rs binary, `models-v1`
  manifest (file-level hashes) — written into an image manifest file consumed by
  `--check`.
* **RC-1**: accept legacy OLE2 Office (`.doc`, `.xls`, `.ppt`) with a compound-file
  magic + stream-count/size limits, route `fast → xberg`, `accurate → docling-rs`,
  and a documented "text + tables only, no page geometry" warning. Fixtures must be
  synthetic or permissively licensed (never customer documents).
* **RC-3**: scan-aware escalation. When `mode=auto` and the fast engine returns an
  OCR-quality warning on a CJK-heavy document, retry once with the accurate engine
  and record `routing.fallback=true` + `escalation_reason` in the IR.
* Resolve licence item **L1** (`libx265`): rebuild without the HEVC encoder or ship the
  GPL source offer. Blocked on legal, tracked in `LICENSE-REVIEW.md`.
* Cap docling-rs RSS (`GOMEMLIMIT`-equivalent is not available: use cgroup memory
  limits per worker and fail fast with `engine_unavailable` on OOM).
* Per-engine timeouts and retry classification tables (memory OOM = retryable once,
  corrupted input = not retryable).
* Emit `metrics.peak_rss_mb` from the subprocess rusage (already implemented).

**Tests.** IR conformance golden files per fixture (stable JSON, no timings);
resource-ceiling test (1.5 GB cgroup) still completes the 30-page fixture.

**DoD.** `docker/Dockerfile.engines` builds reproducibly from pinned inputs;
`docs/ENGINE-BENCHMARK.md` numbers refreshed on the same corpus.

**Status: DONE (PR-04).** Engine artefacts are checksum-pinned (xberg/docling-rs
tarballs + the models script at a fixed commit) and verified in the image build and
in CI; `--engine-max-rss-mb` enforces a per-invocation RSS ceiling and classifies it
as retryable `engine_out_of_memory`; legacy OLE2 Office (`.doc/.xls/.ppt`) is accepted
with a compound-file pre-flight and a `legacy_format` warning; RC-3 scan escalation
retries a CJK scan once with the accurate engine and records `routing.fallback`.
**Not closed here:** L1 (`libx265`) stays BLOCKED on legal; the GPU image is PR-08.

---

## PR-05 — Document IR v0.1 freeze + conformance suite

**Scope**
* Publish `api/document-ir-0.1.schema.json` (JSON Schema) generated from the Go types;
  add a CI check that the schema and the Go structs agree. PR-00 added the fields
  `page_width_pt`/`page_height_pt`/`rotation_deg` to `source_location`; the schema
  must include them.
* Golden IR per engine per fixture (`benchmark/fixtures/golden/*.json`) compared with
  timings normalised out; deliberate differences recorded with `t.Skip` + comment.
* Versioning policy: additive changes keep `0.x`; breaking changes bump `doc-ir/0.x`
  and are announced in `CHANGELOG.md`.
* Fill the two structural gaps that matter downstream: table `formula` extraction
  (XLSX, read from the worksheet `<f>` element — shared formulas, dates and numeric
  precision included) and per-element confidence where the engine provides it.
  PR-00 documented these as *absent capabilities*; the fixtures assert the fields
  stay empty instead of being fabricated.

**Tests.** Golden diff; schema validation of every produced IR; property test that
`reading_order` is strictly increasing and `table_id` references resolve.

**DoD.** `docs/DOCUMENT-IR.md` is normative, and a breaking change fails CI.

**Status: DONE (PR-05), with image artifacts deferred.** Published
`api/document-ir-0.1.schema.json` (normative, drift-checked, validated against real
adapter output and against known-invalid documents); golden Docling→IR conformance
file; XLSX formula + cached-value enrichment from the worksheet XML (shared-formula
slaves warn instead of being guessed). `image_ref` remains reserved: publishing
image artifacts needs a download endpoint plus ACK/TTL participation, which is
storage work (PR-06) — documented rather than half-implemented.

---

## PR-06 — Storage efficiency and quotas

**Scope**
* gzip/br for `result.md`/`result.txt`/`result.json` when the client sends
  `Accept-Encoding`; store compressed, serve transparently (measure the win first).
* **RC-2/RC-4**: capacity work for large spreadsheets — make the expanded-size and
  upload caps configurable per deployment, document the engine hard limits, and
  evaluate sheet-window extraction for 550–770 MiB ledger exports before claiming it.
* Per-owner storage accounting in SQLite (`owner_usage` materialised view updated in
  the same transaction as commit/release).
* Disk-pressure policy: below watermark, reclaim `expired`/`released` tombstones first,
  then reject uploads with `507`; never delete inside TTL (already the rule, add tests).
* Metrics: bytes in/out, reclaim latency p95, orphan sweep counts.

**Tests.** Fill a small tmpfs quota, assert 507 + successful reads; gzip round-trip
byte-identical after decompression; quota accounting matches `du` within 1 %.

**DoD.** `docs/DEPLOYMENT.md` gains a capacity-planning section with measured numbers.

**Status: DONE (PR-06).** Per-owner live-result accounting (`owner_storage`, updated
in the commit/release transactions, `--quota-storage-mb`); gzip on the wire with a
weak validator for the encoded representation (`Vary: Accept-Encoding`, conditional
GET still returns 304); the OOXML expanded-size cap is configurable
(`--max-expanded-mb`) and the capacity section carries the measured corpus numbers.
**Not closed:** sheet-window extraction for 550–770 MiB ledger exports (needs an
engine that can page through a sheet); image artifacts stay reserved.

---

## PR-07 — Cluster-lite for real: multi-host, mTLS, and a load test

**Scope**
* mTLS between api↔coordinator and worker↔coordinator (`--tls-cert/key/ca`), keeping
  the shared token as an additional authorisation layer.
* Worker identity/registration persistence: `workers` table with TTL + capability
  history; `GET /internal/diagnostics/workers` (already exists) fed by it.
* Two-host test: coordinator + 2 api on host A, 4 workers on host B; measure claim
  latency, result throughput, and behaviour under a 30 s network partition.
* Load test with vegeta/k6: 1/10/50 rps of 1-page PDFs, c=1/4/8, report p50/p95,
  coordinator CPU/disk, and where it saturates (upgrade triggers from `CLUSTER-LITE.md`).

**Tests.** Partition → leases expire → jobs recover; mTLS rejects a client cert signed
by the wrong CA; load test produces a report under `benchmark/results/`.

**DoD.** `docs/CLUSTER-LITE.md` numbers are measured, not predicted.

**Status: PARTIAL (PR-07).** Delivered and verified:

* **mTLS** — `internal/tlsutil` plus `--tls-cert/--tls-key/--tls-client-ca` on the
  coordinator and `--tls-ca/--tls-cert/--tls-key/--tls-server-name` on api replicas and
  workers. The shared token stays as an application-level authorisation layer.
  `tests/integration/pr07_tls.py` (8 checks, wired into `make pr00` and the lifecycle
  phase of `scripts/ci-local.sh`) proves against real processes: no client certificate
  → rejected; certificate from a foreign CA → rejected; plaintext HTTP on the TLS port
  → rejected; authorised api replica proxies a job end to end; worker completes a job
  over mTLS; api replica without a client certificate cannot reach the coordinator
  (502); coordinator log contains neither content nor secrets.
* **Load** — `tests/load/loadgen.py` + `scripts/cluster-lite-load.sh` (`make load`)
  start one coordinator, 2 api replicas and 2 workers and measure create→terminal
  latency: **8.84 jobs/s at c=12** (p50 1.23 s, p95 1.53 s) and **8.75 jobs/s at c=64**
  (p50 6.68 s) with zero failures. Two 200 ms mock parsers cap the stack at 10 jobs/s,
  so the API/coordinator path reaches 88 % of the theoretical ceiling and is not the
  bottleneck. Numbers and the reasoning are in `docs/CLUSTER-LITE.md` §7.
* Worker registration persistence and `GET /internal/diagnostics/workers` already
  existed (PR-00); unchanged here.

Not delivered (blocked on hardware, not on design): **two-host** measurement
(A: coordinator + 2 api, B: 4 workers) and a 30 s *network* partition. The partition
path itself is covered by T09 (kill the stack mid-parse → lease expiry → recovery) and
by `TestLease*`; only cross-host latency and MTU behaviour are unmeasured. The
one-command procedure is documented in `docs/CLUSTER-LITE.md` §8 so it can be run on
two hosts without further code changes.

---

## PR-08 — GPU worker image and routing

**Scope**
* Build `parsedock-worker-gpu:0.1.0` (MinerU flash + CUDA base), verify it registers
  `device_type=gpu`, `gpu_model`, `gpu_memory_bytes` and gets `gpu_preferred` work.
* Wire docling-serve CUDA (or drop docling-serve from the GPU story) after confirming a
  supported accelerator option.
* Egress policy: deny `mineru.net`, deny telemetry endpoints; assert with a network test.
* VRAM-aware admission: refuse a claim if free VRAM < engine requirement.

**Tests.** GPU acceptance: `gpu_required` succeeds on GPU worker, `409` without it;
VRAM measured via `nvidia-smi` during the run; egress denied test.

**DoD.** `docs/GPU-REPORT.md` §4 items become PASS with raw evidence.

**Status: PARTIAL (PR-08).** Delivered and verified on the RTX 2080 Ti host:

* `parsedock-worker-gpu:0.1.0` builds (1.0 GB, CUDA 13.2 runtime + MinerU 4.1.2) and
  `tests/integration/pr08_gpu.py` proves 4/4: MinerU runs inside the image, the worker
  registers `device_type=gpu` with **18 832 MiB free VRAM**, a `device=gpu_required`
  scanned-PDF job is routed to `mineru` and completes, and the result carries the
  provenance. Three image defects found by actually running it are documented in
  `GPU-REPORT.md` §4 (broken venv shebang, root-owned mounted volume, `HOME=/root`).
* Free-VRAM reporting on every heartbeat (`-1` unknown, `0` genuinely full) and
  VRAM-aware admission: `--engine-min-vram-mb mineru:8192` makes `gpu_required` return
  `409 gpu_unavailable` naming the requirement when no live GPU worker has that much
  free. Unit-tested by `TestGPUAdmissionRespectsFreeVRAM` (no worker, full worker, enough
  VRAM, unknown VRAM, stale heartbeat).
* MinerU telemetry disabled at build time and by the entrypoint; `--sandbox=unshare-net`
  remains the fail-closed egress control.

Still open: **docling-serve CUDA wiring** (upstream option not documented in the tested
build), a VRAM-refusal measurement on a genuinely full card, and any *external*
distribution of the GPU image, which is blocked by L1 (`libx265`, GPL-2.0) exactly like
the CPU engine image. The script SKIPs cleanly on hosts without a GPU, so it is run on
demand rather than in CPU-only CI.

---

## PR-09 — Observability

**Scope**
* `/metrics` (Prometheus): jobs by state, queue depth, claim latency, parse duration
  by engine, result bytes, retention states, cleanup runs/errors, disk free.
* OpenTelemetry traces: create → claim → engine → commit spans, with engine version
  attributes; no document content.
* **RC-5**: expose the rate-limit and quota counters so a tenant can see why it was
  throttled, and document polling-vs-SSE costs.
* SLO document: availability, p95 latency per mode, and a **result-loss budget for
  committed, not-yet-expired, not-yet-ACKed results** inside the supported failure
  model (single coordinator host, local disk). An ACK *is* a deletion request, so
  "zero loss for acknowledged results" is not a meaningful target (PR-00 / F10).
  Disk loss on the single coordinator host is an explicit single point of failure,
  outside the budget.

**Tests.** Metrics golden file; trace propagation in integration test; SLO doc reviewed.

**DoD.** A dashboard JSON and alert rules for: queue stall, cleanup stalled (already in
`/readyz`), disk watermark, lease-loss rate.

**Status: DONE (PR-09)** — except OTel traces, deliberately exchanged for correlation.

* `internal/metrics`: dependency-free Prometheus exposition (counters, gauges, histograms,
  stable name ordering, label escaping) with unit tests for format, escaping, concurrency
  and label-count safety.
* `/internal/metrics` on the internal listener behind the internal token. Database-derived
  gauges (jobs by state, retention, queue depth, oldest queued age, workers online, disk
  free) are pulled at scrape time, so no write path pays for metrics and one scrape sees one
  consistent state.
* Instrumented paths: job creation, creation→first-claim latency (retries excluded),
  claim→commit parse latency and result bytes per engine, lease expiry, cleanup outcomes,
  and refusals (`rate_limited`, `quota_exceeded`, `insufficient_storage`) from **both** the
  API replicas and the coordinator — this is **RC-5**: a tenant can now see *why* it was
  throttled instead of guessing (`docs/SLO.md` §2, alert `ParseDockThrottleSpike`).
* `docs/SLO.md`: SLO table with the metric behind each target, the result-loss budget
  (0 for committed/not-expired/not-ACKed results inside "coordinator restart on the same
  host"; disk loss and post-ACK deletion are explicitly outside it), the metric reference,
  and measured capacity signals.
* `deploy/observability/dashboard.json` (12 panels) and `alerts.yaml` (7 rules, each naming
  the runbook action): queue stall, no workers, cleanup failing, disk low, lease storm,
  throttle spike, coordinator down.
* `tests/integration/pr09_metrics.py` (5 checks, wired into `make pr00` + lifecycle phase):
  token required, lifecycle/queue/disk/worker series present, parse duration and result
  bytes recorded per engine, a throttled request is counted, and the exposition contains no
  document content, file name or owner id.

**Not delivered:** OpenTelemetry SDK + OTLP export. The dependency cost (licence audit,
collector to operate) outweighed the value given the existing correlation: validated
`X-Request-Id` (stored on the job), audit lines with `request_id`, and the `job_events`
timeline. The propagation point is documented in `SLO.md` §3 so an exporter can be added
without restructuring.

---

## PR-10 — SDKs and contract tests

**Scope**
* Publish `parsedock` Python package (generated client + hand-written lifecycle wrapper)
  and `dev.parsedock:parsedock-client` Java artifact.
* Contract tests generated from `api/openapi.yaml` (schemathesis/pytest) against a live
  stack; include the "ack only after sink" rule.
* Async client variant (`httpx`) and a CLI (`parsedock parse ./file.pdf -o out.md`).

**Tests.** `sdk/python/test_sdk.py` in CI, Java JUnit smoke test, schemathesis run.

**DoD.** Both SDKs versioned with the service and published by release pipeline.

**Status: PARTIAL (PR-10).** Delivered and verified:

* `sdk/python` is an installable package (`pyproject.toml`, `pip install -e sdk/python`) with
  a `parsedock` console script: `parsedock parse ./f.pdf -o out.md`, `parsedock status`,
  `--mode/--device/--format/--keep`, exit codes 0/1/2. The console script and `python -m
  parsedock_sdk.cli` are exercised by the SDK test.
* `--version` prints `{"version":…,"api":…}` as JSON, and `scripts/check_versions.py`
  asserts one version across the binary, `api/openapi.yaml`, `sdk/python`
  (`__version__` **and** `pyproject.toml`) and the Java `pom.xml`.
* The Java client builds with Maven and the jar contains `org/openapitools/client/ApiClient`
  (the generated package name, which the check now asserts exactly after the first version
  wrongly looked for `dev/parsedock/client`).
* `scripts/sdk-check.sh` runs all of it (`make sdk-check`); the Python half runs in the
  PR `unit` phase (`--no-java`), the Java half in the new nightly `sdk-check` job.
* `docs/SDK.md` documents the lifecycle rules the wrapper enforces (ACK only after the sink,
  `result_version` required, idempotent ACK, `410` is expected, polling shares the rate
  budget) and the honest gaps.

**Not delivered:** publishing to PyPI/Maven Central (needs registry credentials and a
licence/notice bundle; the release pipeline still publishes images only), `schemathesis`
property-based contract fuzzing (the repo has 36 OpenAPI-derived contract checks plus a live
end-to-end SDK test; property fuzzing belongs in the nightly job with a pinned version), and
the `httpx` async client (declared as an optional extra but not implemented).

---

## PR-11 — Benchmark corpus and quality bar

**Scope**
* Grow the corpus from 24 synthetic fixtures to 120–200 documents, mixing synthetic and
  permissively licensed public documents (e.g. arXiv PDFs under their licence, government
  open data), with human-checked gold text and table structure.
* Add TEDS (or APTED) for table structure, and per-page CER for long documents.
* Define the release gate: no engine change merges if quality drops > 2 % relative on the
  gold set, or p95 latency regresses > 15 % without an ADR.

**Tests.** `make bench` in nightly CI, results committed under
`benchmark/results/summary/`, regression report in the PR.

**DoD.** `docs/ENGINE-BENCHMARK.md` regenerated automatically; gate enforced.

**Status: PARTIAL (PR-11).** Delivered and verified:

* **Corpus growth**: `benchmark/fixtures/corpus/` now holds **110 synthetic documents**
  (63 PDF incl. 1–30 pages and CJK, 16 CSV varying delimiter/encoding/table size, 16 TXT,
  8 XLSX multi-sheet, 7 DOCX), reproducible via `make fixtures` and hash-checked by
  `tests/unit/test_fixtures.py` (size + SHA-256 per entry, format coverage, and an assertion
  that `test_data` is never committed). Total 2.8 MB, so it lives in the repo.
* **Quality gate**: `benchmark/scripts/gate.py` with a committed
  `benchmark/results/baseline.json`. It fails on >2 % relative quality/structure drop, a CER
  rise of >2 points, or >15 % p50/p95 regression, per engine the baseline covers; the rule is
  relative because absolute numbers are host-specific. `make gate` / `make gate-baseline`;
  wired into `phase_engines` (nightly + engine-changes) and covered by
  `tests/unit/test_gate.py` (pass, quality drop, latency regression, median-only regression,
  millisecond noise, missing sample with and without `--allow-missing`, missing baseline).
  Measurements are **run-scoped** (`PARSEDOCK_BENCH_RAW`/`PARSEDOCK_BENCH_SUMMARY` point at
  `build/ci/bench-*`), so summarize cannot reuse an older successful record; the committed
  baseline records the CI scope (`native,xberg,docling-rs` on 4 fixtures) and a
  baseline-covered sample that disappears fails the gate.
* Table quality is scored as cell + header accuracy, and per-page CER is already in
  `summarize.py`; `ENGINE-BENCHMARK.md` documents what each corpus is allowed to claim.

**Not delivered:** TEDS/APTED tree-edit distance (needs a gold table structure per fixture;
cell+header accuracy is the current discriminator) and permissively licensed *public*
documents (arXiv/government data) in the corpus — the synthetic corpus is safe to ship and
the real corporate corpus stays local-only by policy. Real-engine benchmarking remains in the
nightly/engine-changes pipeline rather than every PR, because installing xberg + docling-rs
takes minutes.

---

## PR-12 — Operational runbooks

**Scope**
* Backup/restore drill for SQLite + file tree (documented + scripted + timed).
  PR-00 landed the offline scripts and a restore test; PR-12 adds the online
  (`.backup`) variant and a timed drill. Backups contain **only active** artifacts:
  a released/expired result must never be resurrected by a restore, and any restore
  must re-apply retention state before serving traffic.
* Coordinator upgrade procedure with zero-result-loss guarantee (drain: stop accepting
  uploads, wait for in-flight, restart).
* Incident playbooks: disk full, cleanup stalled, lease storm, engine OOM loop,
  certificate expiry.

**Tests.** Restore drill executed on the PoC host; RTO/RPO recorded.

**DoD.** `docs/RUNBOOK.md` with commands and expected outputs.

**Status: DONE (PR-12).** Delivered and verified:

* **Online backup** `scripts/backup_online.py`: SQLite backup API on a live WAL database
  (no coordinator stop), file tree copied afterwards and driven by that snapshot, only
  servable (`active`) artifacts copied, `--verify` runs `PRAGMA integrity_check` plus
  per-artifact SHA-256/size, and a JSON report with timings. The offline
  `scripts/backup.sh`/`restore.sh` from PR-00 remain for planned maintenance.
* **Restore drill** `tests/integration/pr12_drill.py` (`make drill`, wired into the CI
  lifecycle phase): online backup of a running stack verifies (5/5), the released job's
  artifacts are absent from the backup, the active result is downloadable with the **same
  `result_version`** after a restore, the released job comes back `released` with `410`, and
  **RTO is measured** (restore + start until `/readyz`: 0.1 s on the PoC host with the mock
  engine; RPO = backup interval, the snapshot itself is consistent).
* **`docs/RUNBOOK.md`**: facts/where-state-lives table, backup/restore with the drill output,
  a zero-result-loss coordinator upgrade (drain → backup → restart → verify), and incident
  playbooks for coordinator down, queue stall, no workers, cleanup failing, disk low, lease
  storm, engine OOM loop, certificate expiry and tenant throttling — each with the metric
  that fires, the commands, and the condition that proves recovery.

**Not delivered:** automated coordinator failover/leader election (out of scope by design:
one stateful coordinator, restart is the documented recovery) and a timed drill on a data
volume large enough to make RTO minutes rather than sub-second — the drill *measures* RTO on
whatever data it is given, so it will report the real number on production-sized data.

---

## Sequencing

```
PR-01 ─▶ PR-02 ─▶ PR-03 ─▶ PR-04 ─▶ PR-05
                    │                  │
                    └────▶ PR-06 ──────┴─▶ PR-07 ─▶ PR-08
                                              │
                                              ├─▶ PR-09 ─▶ PR-10
                                              └─▶ PR-11 ─▶ PR-12
```

PR-01…PR-05 constitute "v0.1 usable"; PR-06…PR-08 "deployable at scale";
PR-09…PR-12 "operable".
