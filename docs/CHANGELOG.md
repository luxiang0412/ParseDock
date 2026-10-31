# Changelog

All notable changes to ParseDock. Versions follow [SemVer](https://semver.org/);
`0.x` is a frozen contract but not yet a stability promise beyond the documented
[Document IR compatibility policy](DOCUMENT-IR.md#2a-compatibility-policy-what-doc-ir01-promises).

## v0.1.0 — 2026-10-31

First tagged release. A complete, self-hosted document-parsing service: async job
API with explicit release, six engine adapters, a browser Playground, Python and
Java clients, and measured SLOs.

### Added

**Service**

* Single Go binary with four roles (`standalone`, `api`, `coordinator`, `worker`);
  stateless API replicas behind any load balancer, one stateful coordinator
  (SQLite in WAL mode + local file store, no Redis/Postgres/S3/MQ).
* Job lifecycle with `queued → running → completed/failed/cancelled`, ACK-driven
  release, TTL expiry, download leases, `410 Gone` tombstones, worker fencing and
  attempt-level retries.
* `mode=auto|fast|accurate`, `device=auto|cpu|gpu_preferred|gpu_required`,
  per-owner quotas (concurrency, jobs/day, bytes/day, storage), per-key scopes
  (read/write/ack), hashed keys, audit lines, `X-Request-Id`.
* SSE event stream (`snapshot`, `created`, `running`, `completed`, `release_pending`)
  with `Last-Event-ID` resume, plus polling with backoff guidance.
* Content-addressed results (`results/<job>/<sha256>/`), per-representation ETags,
  gzip downloads, `--max-expanded-mb` guard, transactional storage accounting.
* Ops: Prometheus exposition (`/internal/metrics`) with scrape-time DB gauges and
  throttling aggregated across API replicas, `/healthz`, `/readyz`, structured
  logs that never contain document text, online verified backups
  (`scripts/backup_online.py`) with a measured restore drill (RTO 0.1 s), and a
  runbook.

**Engines**

* `native` (text/CSV/TSV/Markdown, encoding-aware incl. GB18030/UTF-16),
  `xberg` 1.3.10 (PDF/Office/OCR), `docling-rs` 2.53.3 (layout, tables,
  TableFormer), `docling-serve`, Apache Tika, and MinerU 4.1.2 (`flash`).
* OCR backends: Tesseract (default, in the worker image) and PP-OCRv6 via
  `--xberg-ocr-backend paddle-ocr` (opt-in, models seeded in the image).
* Document IR `doc-ir/0.1`: typed elements, reading order, tables with spans,
  page/bbox provenance, metrics, warnings — with a written compatibility policy.

**Clients and UI**

* Python SDK (`parsedock` package, `parsedock` CLI): retries with `Retry-After`,
  generated idempotency keys, SSE, ACK-after-sink, `navigate`-free errors carrying
  a stable `code` and `retryable` flag.
* Java client (`dev.parsedock:parsedock-client`, generated from the OpenAPI
  document, unknown-enum tolerant).
* Playground: React 19 + Vite + Tailwind single-page comparison view (original vs
  parsed Markdown) with page-wide drag and drop, SSE progress, explicit release,
  PDF/image/CSV/DOCX/XLSX preview via pdf.js, docx-preview, exceljs and PapaParse,
  bilingual EN / zh-CN, served behind a same-origin proxy that injects the API key
  so it never reaches the browser.
* Runnable examples: `examples/python`, `examples/java` (`make run`).

**Tooling**

* Docker Compose layouts for a single host (4 containers, two published ports),
  a CPU stack, an optional GPU worker profile, and a cluster-lite reference with
  optional mTLS.
* CI: merge gate (static, unit, contract, lifecycle, image, web, sdk), nightly
  real-engine acceptance, SBOM, SDK and Playground jobs; reproducible build pins
  (`images.lock.json` + `verify_pins.py`).

### Changed (release hardening)

* `POST /v1/parse` **no longer releases** the result it returns. A written
  response is not proof of receipt; the result now follows the TTL and the caller
  ACKs with `X-Result-Version` (already returned in the response headers).
* `POST /v1/jobs/{id}/cancel` **never releases** a completed result: it returns
  `409` and points at `POST /v1/jobs/{id}/ack`. One release path, no cancel/ACK race.
* `create_job` always sends an `Idempotency-Key` (generated when the caller omits
  one), so a retried upload whose response was lost cannot create a second job.
* `wait_released` raises `TimeoutError` instead of returning a still-pending job.
* `Retry-After` is honoured; retryability is code-first (`quota_exceeded` shares
  the `429` status with `rate_limited` but is not retried).
* `429` documented on every rate-sharing operation; `processing_status` marked
  deprecated in favour of `status`.

### Fixed

* Tables from Office documents no longer sink to the end of the Markdown: they are
  placed where their rows were (xberg) and anchored at their caption via docling's
  header cells, with the reading order taken from the right source per input type.
* OCR quality signals are no longer conflated: `ocr_used` / `ocr_quality_unknown` /
  `ocr_low_confidence` are separate, automatic escalation fires only on measured
  evidence, and a good PP-OCRv6 result is no longer re-parsed. Real gains on the
  reported document: Chinese scan CER 0.075 → 0.000; `auto` no longer double-parses.
* Second-pass escalation now charges wall time when the retry fails.
* `gpu_required` no longer reports success for CPU execution: per-task GPU evidence
  is required (the GPU worker image's MinerU path is CPU-only on this host and now
  fails closed with `gpu_unavailable`).
* Result ordering, table de-duplication, PDF tail/`/Encrypt` detection, page cap
  raised to 2000, worker poll interval 250 ms, relative `--work-dir` fixed.

### Known limitations

* **Worker image redistribution is blocked** on licence item L1 (`libx265`, GPL-2.0)
  inside the xberg distribution. The control-plane image (95 MB) is unaffected.
* GPU execution is **not** proven on the current test host: MinerU's Vulkan path
  cannot initialise inside the container (driver/toolkit interaction, see
  `OCR-PR-D2-REPORT.md`); docling-rs CUDA is 15–18× slower than CPU there. The
  recommended GPU route is docling-serve with CUDA.
* Tables inside **scanned** pages are not structured by the default engine
  configuration; documents with 500+ MiB expanded XLSX sheets fail with
  `expanded_too_large`; encrypted documents fail with `encrypted_file` (password
  support is a product decision).
* Python and Java clients are **not published** to PyPI/Maven Central yet; install
  from the repository (see `docs/SDK.md`).
* No multi-host failover: a coordinator restart recovers, but there is no automatic
  promotion.
