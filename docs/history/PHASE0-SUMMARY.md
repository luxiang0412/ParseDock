# PHASE 0 — final report

> **Historical snapshot (2026-10-30, pre PR-00).** The scores quoted here were
> recomputed by PR-00 with a corrected rubric and measurement pipeline; read
> `docs/ENGINE-DECISION.md` §2 / `docs/ENGINE-BENCHMARK.md` §7 for the authoritative
> numbers, and `docs/history/PR00-ISSUE-MATRIX.md` for what changed.

Deliverable of the Phase 0 task (`docs/briefs/document-parsing-phase0-guide-2026-10-29.md`).
Every claim below links to a reproducible artefact in this repository.

---

## 1. Environment and versions

| Item | Value |
| --- | --- |
| Host | Ubuntu 24.04.5, kernel 7.0.0-42, i7-13700K (24 threads), 31 GiB RAM, NVMe 1.9 TB |
| GPU | RTX 2080 Ti 22 528 MiB, driver 595.99.02, CUDA 13.2, compute capability 7.5 |
| Container | Docker 29.9.2, Compose v5.6.1, cgroup v2, `nvidia` runtime + CDI |
| Toolchain | go1.24.4, Python 3.12.3, OpenJDK 8, sqlite (via modernc, pure Go) |
| Engines | xberg 1.3.10 · docling-rs 2.53.3 (ONNX Runtime 1.31) · Docling 2.145.0 · docling-serve 1.38.2 · MinerU 4.1.2 · Apache Tika 4.2.0 (`apache/tika@sha256:4774ea…`) |
| Images built | `parsedock:0.1.0` (94.8 MB, `7d297f009e9f`), `parsedock-worker-cpu:0.1.0` (1.19 GB, `b83af36161c1`) |
| Repo | single Go module, 4 roles, ~6.5 kLOC Go + harnesses; see `README.md` |

Full detail: `docs/ENVIRONMENT.md`.

## 2. Candidate engine results

| Engine | Quality (CER en/zh, scans) | Structure | Latency (warm, median) | RSS | Licence outcome |
| --- | --- | --- | --- | --- | --- |
| xberg 1.3.10 | 0.116 / 0.440; scans 0.055 (EN), 0.824 (ZH); GB18030 CSV mojibake | table grid + bbox, no sheet names | 10–26 ms native, 217–704 ms OCR | 39–232 MB | MIT core, **GPL `libx265` in the release tarball** → open item L1 |
| docling-rs 2.53.3 | **0.007 / 0.034**; scans 0.884 (EN), 0.098 (ZH) | full body tree, bbox + origin, spans, sheet names | 277–1 046 ms | 818–1 167 MB | MIT + permissive models |
| docling-serve 1.38.2 | same as Docling; scans empty with OCR off | same | **constant ≈ 2.0 s/request** | 7.2 GB env | MIT |
| MinerU 4.1.2 | 0.167 / 0.628; scans 0.664 / 0.451 | markdown + page markers, no bbox | 0.86–1.6 s | 54 MB + ~880 MB VRAM | Apache-2.0 **+ extra terms**; only `flash` is local, VLM weights AGPL |
| Tika 4.2.0 | 0.123 / 0.510; scans 0.199 / 1.255 | none (text only) | 5–347 ms, bimodal | 1.37 GB image | Apache-2.0 |
| native (Go) | 0.000 on csv/tsv/txt/md incl. **GB18030** | table grid, markdown structure | < 1 ms | process | in-house |

Notable failures/limits found: Tika accepted a 40 MiB zip bomb (HTTP 200, 14 ms) —
ParseDock's own pre-flight is the guard; xberg cannot expose XLSX sheet names; no
engine gives per-element OCR confidence/word boxes in the tested configuration.

Details: `docs/ENGINE-CANDIDATES.md`, `docs/ENGINE-BENCHMARK.md`.

## 3. Chosen technology

* **Control plane:** Go, single binary, roles `standalone|api|coordinator|worker`,
  stdlib HTTP + `modernc.org/sqlite` (WAL, pure Go, no CGO) + `x/text`.
* **Engines (v0.1):** `native` for text, **xberg = `mode=fast`**,
  **docling-rs = `mode=accurate`**; Tika as opt-in fallback; MinerU `flash` as the
  optional GPU engine. docling-serve dropped from the hot path (2 s/request, 7.2 GB
  runtime) but kept as an adapter and conformance oracle.
* **Contract:** `doc-ir/0.1` is the source of truth; Markdown/Text are rendered by
  the coordinator; every result records engine, version and `routing.reason`.
* **Weighted score** (quality 40 / perf 25 / resources 15 / coverage 10 / ops 10):
  xberg 8.2, docling-rs 6.3, Tika 5.8, MinerU 5.4, docling-serve 4.5 (superseded by
  PR-00: xberg 8.04, docling-rs 6.45, Tika 6.90, MinerU 5.73, docling-serve 5.03) — the split
  routing, not a single winner. Arithmetic in `docs/ENGINE-DECISION.md`.

## 4. Architecture and protocol

* **Standalone:** coordinator + api + worker in one process, two listeners, the
  worker talking to the local coordinator over HTTP (same code path as cluster).
* **Cluster-lite:** N stateless API replicas (reverse proxy, no state/files) → one
  coordinator (SQLite + files) → M workers pulling with lease tokens. Fencing on
  every result/heartbeat; expired leases requeue with backoff.
* **Lifecycle:** `processing_status` and `retention_status` are independent
  dimensions; ACK requires `result_version` (manifest hash), is idempotent, and only
  moves the job to `release_pending`; deletion happens in the cleaner after live
  download leases end; `410 Gone` during the tombstone window, then `404`.
* **Timers:** result TTL from **commit**, absolute `max_lifetime`, worker lease +
  heartbeat, download lease renewed while streaming, 10 min tombstone.
* **Worker API:** register/claim/input/heartbeat/result/fail, internal token + task
  lease token, `X-Content-SHA256` verified on download, IR re-rendered server-side.
* **Security:** magic-bytes + OOXML structure + zip-ratio + PDF marker/page checks,
  server-side names, owner isolation (`404` for foreign jobs), rate limits, upload
  caps, disk watermark, subprocess isolation with fail-closed `unshare -n`.
* **Docs:** `ARCHITECTURE.md`, `JOB-LIFECYCLE.md`, `DOCUMENT-IR.md`,
  `CLUSTER-LITE.md`, `DECISIONS.md`, `deploy/*.yaml`, `api/openapi.yaml`.

## 5. Acceptance results (T01–T16)

Latest run: `benchmark/results/raw/acceptance-lifecycle-20261030T161753.json`
and `acceptance-real-20261030T161807.json`.

| ID | Scenario | Result | Evidence |
| --- | --- | --- | --- |
| T01 | upload + parse a PDF | **PASS** | 3-page EN PDF completed via xberg/docling-rs, markdown contains title + figures, IR has 3 pages + table, `ETag` = `Content-Length` consistent |
| T02 | DOCX/XLSX/CSV/image | **PASS** | all four parsed; non-whitelisted content returns 415 with `unsupported_type` |
| T03 | download then ACK | **PASS** | 202 → 410 → files removed → `released` |
| T04 | never ACK → TTL | **PASS** | `expired`, 410 inside tombstone, row purged to 404 afterwards |
| T05 | ACK during download | **PASS** | 8 MiB stream held open; ACK 202; new download 410; cleaner skipped the busy job; files removed after lease end |
| T06 | duplicate ACK | **PASS** | both 202, second job untouched and still downloadable |
| T07 | ACK vs TTL race ×4 | **PASS** | single terminal state each time, zero orphan files |
| T08 | SIGKILL during cleanup | **PASS** | 6 files remained at kill, all removed after restart, all jobs `released` |
| T09 | worker SIGKILL | **PASS** | attempt 1 `expired` → attempt 2 `succeeded`; stale result/heartbeat `409` |
| T10 | two API replicas | **PASS** | create on A, query/download/SSE/ACK on B, same `result_version` |
| T11 | CPU-only node | **PASS** | xberg/docling-rs parse with no GPU in path; container stack also CPU-only |
| T12 | `gpu_required` without GPU worker | **PASS** | `409 gpu_unavailable`, **no job row created** |
| T13 | invalid/adversarial uploads | **PASS** | empty, corrupt, wrong-ext, encrypted, zip bomb, fake zip all 4xx; service stayed healthy |
| T14 | disk watermark | **PASS** | new upload `507`; existing result still 200; restored after restart |
| T15 | stale attempt writes | **PASS** | late result and heartbeat both `409`; version/page count unchanged |
| T16 | released/expired → 410 | **PASS** | 410 for both; foreign owner gets 401/404 without existence leak |
| GPU | GPU verification | **PARTIAL** | CUDA + VRAM measured natively (Docling 30 p: 13.8 s CPU → 7.5 s CUDA, +1.5 GB); GPU container plumbing verified with `nvidia-smi`; **GPU worker image not built** (`docs/history/GPU-REPORT.md` §4) |
| Container | CPU compose stack | **PASS** | coordinator + 2 api + worker; xberg and docling-rs parsed; ACK on one replica → 410 on the other |

Also: `go test ./...` (storage sniffing, atomic store, IR rendering, engine ladders,
coordinator lifecycle/fencing/cleanup) and `sdk/python/test_sdk.py` (9 checks incl.
"failing sink does not release") pass.

## 6. Risks and open items

| # | Risk | Impact | Next step |
| --- | --- | --- | --- |
| R1 | xberg tarball bundles `libx265` (GPL-2.0) | cannot ship that bundle as-is | rebuild without HEVC encoder or ship GPL source offer (**L1**) |
| R2 | MinerU licence addendum + attribution | legal obligation for external services | implement attribution or don't expose MinerU publicly |
| R3 | MinerU remote tiers upload documents | violates the no-third-party rule | hard-coded `flash` + egress deny (verified) |
| R4 | Coordinator is a single writer and the file bottleneck | throughput ceiling, restart point | measured triggers in `CLUSTER-LITE.md` §5; PR-06/PR-07 |
| R5 | docling-rs 0.8–1.2 GB RSS | OOM on small nodes | cgroup ceiling + concurrency 1 (PR-04) |
| R6 | OCR quality: docling-rs 0.884 CER on the synthetic EN scan, xberg 0.82 on ZH | wrong text for bad scans | route scans per corpus measurement; add real scans (PR-11) |
| R7 | No per-element OCR confidence/word boxes | cannot show citation boxes for scans | engine-level feature work |
| R8 | Shared static internal token, no mTLS | lateral movement risk | PR-07 |
| R9 | Corpus is 24 synthetic files, not 120–200 | selection confidence | PR-11 |
| R10 | docling-serve 2 s/request and no GPU switch wired | unusable as a hot-path service | keep out of hot path; revisit in PR-08 |

## 7. Next-phase work (independent PR slices)

| PR | Scope | Definition of done |
| --- | --- | --- |
| PR-01 | CI: vet/test/build, OpenAPI validation, SDK drift, mock acceptance per PR | green pipeline < 8 min with raw artefacts |
| PR-02 | Key hashing/rotation, scopes, per-owner quotas, audit log | no plaintext tokens; quota tests |
| PR-03 | Cooperative cancel + per-engine wall clock | cancel within 2 heartbeats, no artifact |
| PR-04 | Engine productionisation, pinned digests, memory ceilings, L1 licence fix | reproducible image manifest |
| PR-05 | `doc-ir/0.1` JSON Schema + golden conformance suite | breaking change fails CI |
| PR-06 | Compressed results, per-owner storage accounting, pressure policy | capacity section with measurements |
| PR-07 | mTLS, two-host cluster-lite test, load test | measured cluster numbers |
| PR-08 | GPU worker image + VRAM-aware admission | GPU report §4 all PASS |
| PR-09 | Prometheus/OTel metrics, SLOs, dashboards | dashboard + alert rules |
| PR-10 | Published Python/Java SDKs + schemathesis contract tests | SDKs released with service |
| PR-11 | 120–200 document corpus, TEDS, regression gate | nightly bench with gate |
| PR-12 | Backup/restore + incident runbooks | restore drill timed |

Details: `docs/IMPLEMENTATION-PLAN.md`.

## 8. How to reproduce

```bash
make build && make test          # binary + unit/integration tests
make acceptance                  # T03–T16 (mock engine)
make acceptance-real             # T01/T02/T11 (xberg + docling-rs)
make sdk-test                    # Python SDK lifecycle
make images && make up           # CPU compose stack (real engines)
make bench && make gpu-bench     # quality/latency/concurrency + GPU
```

Raw data: `benchmark/results/raw/*.jsonl`; tables:
`benchmark/results/summary/benchmarks-{quality,perf,concurrency}.csv`.
