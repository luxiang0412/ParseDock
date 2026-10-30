# IMPLEMENTATION PLAN — Phase 1 (post-Phase-0)

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

---

## PR-04 — Engine productionisation: xberg (fast) + docling-rs (accurate)

**Scope**
* Pin artefacts by digest: xberg tarball sha256, docling-rs binary, `models-v1`
  manifest (file-level hashes) — written into an image manifest file consumed by
  `--check`.
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

---

## PR-05 — Document IR v0.1 freeze + conformance suite

**Scope**
* Publish `api/document-ir-0.1.schema.json` (JSON Schema) generated from the Go types;
  add a CI check that the schema and the Go structs agree.
* Golden IR per engine per fixture (`benchmark/fixtures/golden/*.json`) compared with
  timings normalised out; deliberate differences recorded with `t.Skip` + comment.
* Versioning policy: additive changes keep `0.x`; breaking changes bump `doc-ir/0.x`
  and are announced in `CHANGELOG.md`.
* Fill the two structural gaps that matter downstream: table `formula` extraction
  (XLSX) and per-element confidence where the engine provides it.

**Tests.** Golden diff; schema validation of every produced IR; property test that
`reading_order` is strictly increasing and `table_id` references resolve.

**DoD.** `docs/DOCUMENT-IR.md` is normative, and a breaking change fails CI.

---

## PR-06 — Storage efficiency and quotas

**Scope**
* gzip/br for `result.md`/`result.txt`/`result.json` when the client sends
  `Accept-Encoding`; store compressed, serve transparently (measure the win first).
* Per-owner storage accounting in SQLite (`owner_usage` materialised view updated in
  the same transaction as commit/release).
* Disk-pressure policy: below watermark, reclaim `expired`/`released` tombstones first,
  then reject uploads with `507`; never delete inside TTL (already the rule, add tests).
* Metrics: bytes in/out, reclaim latency p95, orphan sweep counts.

**Tests.** Fill a small tmpfs quota, assert 507 + successful reads; gzip round-trip
byte-identical after decompression; quota accounting matches `du` within 1 %.

**DoD.** `docs/DEPLOYMENT.md` gains a capacity-planning section with measured numbers.

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

---

## PR-09 — Observability

**Scope**
* `/metrics` (Prometheus): jobs by state, queue depth, claim latency, parse duration
  by engine, result bytes, retention states, cleanup runs/errors, disk free.
* OpenTelemetry traces: create → claim → engine → commit spans, with engine version
  attributes; no document content.
* SLO document: availability, p95 latency per mode, result-loss budget (must be zero
  for acknowledged results).

**Tests.** Metrics golden file; trace propagation in integration test; SLO doc reviewed.

**DoD.** A dashboard JSON and alert rules for: queue stall, cleanup stalled (already in
`/readyz`), disk watermark, lease-loss rate.

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

---

## PR-12 — Operational runbooks

**Scope**
* Backup/restore drill for SQLite + file tree (documented + scripted + timed).
* Coordinator upgrade procedure with zero-result-loss guarantee (drain: stop accepting
  uploads, wait for in-flight, restart).
* Incident playbooks: disk full, cleanup stalled, lease storm, engine OOM loop,
  certificate expiry.

**Tests.** Restore drill executed on the PoC host; RTO/RPO recorded.

**DoD.** `docs/RUNBOOK.md` with commands and expected outputs.

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
