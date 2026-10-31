# Changelog

All notable changes to ParseDock. Versions follow [SemVer](https://semver.org/);
`0.x` is a frozen contract but not yet a stability promise beyond the documented
[Document IR compatibility policy](DOCUMENT-IR.md#2a-compatibility-policy-what-doc-ir01-promises).

## Unreleased

### Upgrade notes (read first)

* **Compose requires `PARSEDOCK_WORKER_TOKEN`.** The reference compose files refuse to
  start without a second random token for the workers (distinct from
  `PARSEDOCK_INTERNAL_TOKEN`): add `PARSEDOCK_WORKER_TOKEN=$(openssl rand -hex 24)` to an
  existing `.env`. The binary itself still runs without it (startup warning).
* **Plaintext API keys shorter than 16 characters fail startup** (only a warning under
  `--dev-insecure`); a token mapped to two owners fails startup too. `sha256:` entries are
  unaffected.
* **Invalid numeric/duration environment values are startup errors** (e.g.
  `PARSEDOCK_WORKER_LEASE=60` without a unit) instead of a silent fallback to the default.
  An explicitly set `--heartbeat` must be shorter than `--worker-lease` on the coordinator.
* **`coordinator_unavailable` is `503`** at the gateway (it was `502`), as the OpenAPI spec
  always documented. The Python SDK retries both.
* **Stricter request validation:** unparsable `priority`, `priority` outside `0..100` and
  `ttl_seconds < 1` → `400 invalid_request`; an `Idempotency-Key` longer than 200
  characters → `400` (it was silently truncated, so two keys sharing a prefix replayed the
  wrong job); a cancel that loses the race to completion → `409` (it answered `202`).
* **`/readyz` is redacted** for callers without the internal token: verdicts only, no
  `free_bytes`, `last_cleanup` or error text.
* **The Playground binds `127.0.0.1`** by default; set `PLAYGROUND_BIND=0.0.0.0` to expose it.
* **Go 1.27.3** (was 1.24.4) for builds and images; `GOTOOLCHAIN=local` in CI.

### Security

* **Separate worker credential** (`--worker-token` / `PARSEDOCK_WORKER_TOKEN`). With it,
  workers can call only the worker protocol; `/v1` (which asserts owners) and the
  operator endpoints accept only the internal token and answer the worker token with
  `403`. Optional and backward compatible in the binary (warning when unset); the
  reference compose files now **require** it and give workers nothing else — add one
  more `openssl rand -hex 24` to an existing `.env`.
* Standalone generates random in-process internal/worker tokens when none are set, so
  its coordinator listener is no longer open to every local process.
* `/readyz` returns verdicts only to unauthenticated callers; `free_bytes`,
  `last_cleanup` and error text need the internal token. `checks.cleanup` is always
  present now.
* Gateway: failed authentication is rate limited per client address (`429`); limiter
  maps are bounded; `X-Debug-Owner` is validated; plaintext API keys under 16
  characters fail startup (unless `--dev-insecure`) and one token for two owners fails
  startup; the client's own key is no longer forwarded to the coordinator.
* Client IP reaches the coordinator's audit lines (`client_ip`); `X-Forwarded-For` is
  believed only from `--trusted-proxies`.
* Per-request body read deadline (`--body-read-timeout`, default 10 min).
* Playground: binds `127.0.0.1` by default (`PLAYGROUND_BIND`), keeps CSP/XFO/nosniff on
  `index.html`, clears `X-Api-Key`; control-plane containers drop all capabilities.

### Fixed

* Synchronous `POST /v1/parse` longer than 30 s returned `502` at the gateway (shared
  30 s response-header timeout); the gateway proxy now waits 150 s.
* Malformed numeric/duration environment values are a startup error instead of a
  silent fallback to the default; `--heartbeat` must be shorter than `--worker-lease`.
* `--rate-limit=0` disables the per-owner limiter (it was reset to 20).
* Standalone with TLS used `http://` for its in-process clients; standalone now waits
  for gateway, worker and coordinator (in that order) before closing the database.
* `deploy/host`: the worker now gets the 128 MiB upload limit too (it refused anything
  above its own 64 MiB default); cluster-lite's coordinator port is reachable by remote
  workers (it was only on an internal network).
* `X-Api-Key` is documented in `api/openapi.yaml` (`apiKeyAuth`).
* Worker: a single failed heartbeat no longer cancels a healthy parse (a network blip or a
  coordinator restart used to fail the job as non-retryable `cancelled`); only an explicit
  `cancelled`/`lease_lost` stops the attempt, other failures are retried until the lease
  would have expired. A parse deadline is reported as a timeout, not `cancelled`.
* Engines: OOM kills are classified as `engine_out_of_memory` (now in the OpenAPI enum and
  the SDKs); the CLI runner bounds its wait and reaps the whole process group; HTTP engine
  deadlines and cancellations map to the right codes; oversized Tika output is refused.
* Coordinator: `/v1/parse` failures carry the HTTP status their code has everywhere else
  (`quota_exceeded` is `429` with `Retry-After`, a cancelled job `409`); 500 responses no
  longer contain internal paths or driver messages (details are logged with the request id);
  worker-reported error codes outside the documented set become `internal_error`; SSE no
  longer ends without the `completed` event when a completion races the poll; retention
  finalisation is compare-and-set; quota and cleanup reads fail closed.
* When `--heartbeat` is left at its default and `--worker-lease` is not longer than it, the
  heartbeat defaults to a third of the lease instead of letting every lease expire.

### Changed

* **Markdown view fidelity.** Numbered lists keep their numbers (`list_marker`, or
  numbered per run), nested items are indented to their parent's content column, quotes
  render as `>`, code fences carry their language, pipe-table cells keep line breaks as
  `<br>` and escape `|`, and tables with merged cells or several header rows render as an
  HTML `<table>` with `rowspan`/`colspan` (text HTML-escaped). Plain text is escaped where
  CommonMark would reinterpret it. Page furniture (`header`/`footer` elements and
  `content_layer=furniture`) no longer appears in the Markdown **or text** body; it stays
  in JSON.

### Added

* **Document IR `rich_text`** (additive, still `doc-ir/0.1`): elements and table cells may
  carry the same content as `text` with CommonMark inline markup (`**bold**`, `*italic*`,
  `` `code` ``, `[text](url)`, `~~strike~~`). `text` stays plain, so the text view, search
  and scoring are unchanged. Defined metadata keys are documented in
  `docs/DOCUMENT-IR.md` (`content_layer`, `markdown`, `list_ordered`, `list_marker`,
  `code_language`). Both SDKs and the playground types are regenerated.
* `docs/ERROR-CODES.md`: every stable error code with HTTP status, retryability, meaning and
  origin; the contract test fails unless it, `internal/jobs` and the OpenAPI enum agree.
* CI `static` phase: pinned `golangci-lint`, `govulncheck` (run on the pinned toolchain;
  0 reachable vulnerabilities on go1.27.3) and `ruff`; `make lint`, `make vulncheck`.

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
* Java client (`com.luxiangs:parsedock-client`, generated from the OpenAPI
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
  `history/OCR-PR-D2-REPORT.md`); docling-rs CUDA is 15–18× slower than CPU there. The
  recommended GPU route is docling-serve with CUDA.
* Tables inside **scanned** pages are not structured by the default engine
  configuration; documents with 500+ MiB expanded XLSX sheets fail with
  `expanded_too_large`; encrypted documents fail with `encrypted_file` (password
  support is a product decision).
* Java client: the 0.1.0 bundle is validated in the Central Portal and needs the
  one-click **Publish** before `com.luxiangs:parsedock-client` resolves from Maven
  Central; the Python package is live on PyPI (`pip install parsedock`).
* No multi-host failover: a coordinator restart recovers, but there is no automatic
  promotion.
