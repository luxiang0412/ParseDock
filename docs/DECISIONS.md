# DECISIONS (ADR log) — Phase 0

Format: context → decision → consequences → evidence / revisit trigger.
Status values: **Accepted**, **Provisional** (needs more data), **Rejected**.

---

## ADR-001 — Single binary with `--role` (not four binaries)

**Context.** Coordinator, API and worker share types (job model, IR, storage, engine
registry). Separate binaries would duplicate wiring and slow the feedback loop. The
task guide asked for an explicit comparison.

**Decision.** One `cmd/docparse` with `--role standalone|api|coordinator|worker`,
one container image (`parsedock:0.1.0`, 94.8 MB) reused by all roles. The engine
adapters are built into the binary but activated by flags, so a coordinator image
never contains engine code paths it could accidentally execute.

**Consequences.**
* + one build, one image, identical code path between standalone tests and clusters;
  `--check` prints the effective config/capabilities for support.
* − every image carries all adapters (they are thin clients; weight comes from the
  external binaries, which are only in the worker image).
* − a compile error in any adapter breaks all roles (accepted: single module anyway).

**Evidence.** `docker/Dockerfile`, `docker/Dockerfile.engines`, `cmd/docparse/main.go`.

---

## ADR-002 — API is a reverse proxy, not an RPC client

**Decision.** API replicas authenticate, rate-limit, cap upload size and reverse-proxy
to the coordinator's internal `/v1` listener with `X-Internal-Token` + `X-Parsedock-Owner`.

**Consequences.** No buffering of results or SSE; "no sticky session" is structural;
the coordinator must be reachable only on a private network (enforced in compose by an
`internal: true` network). Public port never reaches the coordinator.

**Evidence.** `internal/api/server.go`; T10 (create on A, download/ACK/SSE on B).

---

## ADR-003 — SQLite WAL as the queue and state store

**Decision.** One SQLite file, WAL, `busy_timeout=5000`, `synchronous=FULL`, short
transactions, no I/O inside transactions. Claim is a single atomic
`UPDATE … WHERE id=(SELECT …) RETURNING`.

**Consequences.** Zero external infrastructure; one writer host (documented
non-goal to share it). Write amplification is bounded by heartbeat throttling.

**Revisit trigger.** `wal` growth > 1 GB/h, claim latency p95 > 50 ms, or > ~50 workers
per coordinator. Then: separate queue (NATS/Postgres) or shard coordinators by tenant.

**Evidence.** `migrations/0001_init.sql`, `internal/coordinator/service.go` (Claim),
T08/T09.

---

## ADR-004 — Two lifecycle dimensions + tombstone

**Decision.** `processing_status` and `retention_status` are separate columns with
compare-and-set transitions; released/expired rows live for `tombstone_ttl` so clients
get `410 Gone` instead of an ambiguous `404`.

**Consequences.** No "ACK means deleted" lie; cleanup is idempotent and crash-safe;
a job can be `failed`+`release_pending` at the same time, which is correct.

**Evidence.** `docs/JOB-LIFECYCLE.md`; T03/T04/T07/T08/T16.

---

## ADR-005 — ACK carries `result_version`; downloads hold leases

**Decision.** First ACK must present the manifest hash; a mismatch is `409`.
Every download creates a `download_leases` row that the cleaner respects.

**Consequences.** A client can prove which bytes it consumed; ACK-during-download never
truncates a live stream (T05 with an 8 MiB in-flight body). The cost is one extra
table and a cleaner rule.

**Rejected alternative.** "Delete on ACK immediately" — would break in-flight reads and
make retries destructive.

---

## ADR-006 — IR-first (`doc-ir/0.1`), Markdown as a derived view

**Decision.** Engines produce the IR; the coordinator renders markdown/text/json.

**Consequences.** One renderer, consistent output across engines, provenance preserved,
and engine differences become visible (warnings) instead of dialect soup.
Cost: adapters must map their document model (done for Docling, xberg, MinerU, Tika, native).

**Evidence.** `docs/DOCUMENT-IR.md`, `internal/documentir/*`.

---

## ADR-007 — Engine routing by `mode`/`device`, never by engine name from the client

**Decision.** Clients send `mode=auto|fast|accurate` and
`device=auto|cpu|gpu_preferred|gpu_required`; the coordinator picks an engine and
records `routing.reason` + actual engine/version in the result.

**Consequences.** No engine lock-in for callers; `gpu_required` never silently degrades
(`409 gpu_unavailable` when no GPU worker with a matching engine is registered).
Modes are explicit: an `auto`-only engine cannot satisfy `accurate`.

**Evidence.** `EngineSpec.Supports`, T12.

---

## ADR-008 — First production engine pair: xberg (fast) + docling-rs (accurate)

**Decision.** See `docs/ENGINE-DECISION.md` for the weighted scoring.

**Consequences.** CPU-only deployments work with both. Python Docling stays as a
conformance oracle; docling-serve is optional; Tika is a fallback; MinerU is optional
and `flash`-only.

**Revisit trigger.** Any of: docling-rs accuracy regression > 10 % relative on the gold
set, xberg licence decision blocking distribution, or a GPU engine beating docling-rs on
the 20 % of hardest documents at acceptable cost.

---

## ADR-009 — MinerU is restricted to the local `flash` tier

**Decision.** The adapter hard-codes `--tier flash`, rejects other tiers, and the
deployment must block `mineru.net` egress. Telemetry disabled at image build.

**Why.** Verified in Phase 0: only `flash` runs locally; other tiers upload documents
to a third-party service (violates the "no third-party upload" constraint), and the
MinerU VLM weights are AGPL-3.0 while flash uses Apache-2.0 Paddle models.

**Evidence.** `docs/ENGINE-CANDIDATES.md` §5, `docs/LICENSE-REVIEW.md` §3,
`internal/engines/mineru.go`, `mineru parse --tier basic` → "Local parse-server is
disabled. Use --tier flash or --remote."

---

## ADR-010 — Sandboxing fails closed

**Decision.** If `--sandbox=unshare-net` is requested but namespaces are unavailable
(container without `CAP_SYS_ADMIN`), the engine call returns `engine_unavailable`
rather than running unsandboxed.

**Consequences.** Compose does not enable the flag by default; it is a documented
hardening option. A misconfiguration is loud, not silent.

**Evidence.** Observed in the first compose run (`unshare: Operation not permitted` →
job failed with an explicit code), then `ErrSandboxUnavailable` added in
`internal/engines/exec.go`.

---

## ADR-011 — No automatic ACK in the server; SDK `download_and_ack()` is opt-in

**Decision.** The server never infers consumption from a 200. SDKs do it only after the
caller's sink succeeded.

**Why.** "HTTP 200 delivered" ≠ "client stored it". Auto-ACK would destroy results on
client crashes.

**Evidence.** `docs/JOB-LIFECYCLE.md` §4, `sdk/python/parsedock_sdk/client.py`.

---

## ADR-012 — ULID-style ids, prefix-separated by type

**Decision.** `job_`, `dl_` (download lease), `lt_` (lease token), `wrk_` (worker):
48-bit millisecond timestamp + 80 random bits, Crockford base32.

**Why.** Sortable by creation, no dependency, distinguishable in logs, no PII.

**Rejected.** UUIDv4 (not sortable, no type prefix), DB sequences (couples ids to the DB).

---

## ADR-013 — Upload + create job are one call (multipart)

**Decision.** `POST /v1/jobs` streams the file and enqueues in one request;
`Idempotency-Key` makes retries safe. No presigned-URL flow in P0.

**Consequences.** One round trip, no orphan uploads, atomic validation. Chunked/resumable
upload can be added later without object storage, but needs its own contract.

---

## ADR-014 — No remote URL ingestion, no webhooks in P0

**Decision.** Rejected for Phase 0. A URL feature needs SSRF controls (DNS rebinding,
private ranges, redirects, size/time bounds); webhooks need HMAC, replay protection
and callback-IP policy. Both are P1 with their own design review.

---

## ADR-015 — Measured, not asserted, performance claims

**Decision.** All numbers in `docs/ENGINE-BENCHMARK.md` come from
`benchmark/results/raw/*.jsonl` produced by `benchmark/scripts/bench.py` on the host
described in `docs/ENVIRONMENT.md`, with 5 repeats, warm-up separated, and
xberg's on-disk cache disabled (otherwise sub-10 ms "parses" are cache hits).

**Consequence worth recording:** the first measurement round was wrong because
`/usr/bin/time -f %e` has 10 ms resolution and xberg caches results. Both were fixed
before any number was published; the flawed run is still in `results/raw/` for audit.

---

## ADR-016 — OCR backend selection is opt-in; PP-OCRv6 for CJK, Tesseract for Latin

**Decision (2026-10-31, spike).** Keep `tesseract` as the default OCR backend and
make any change opt-in:

* `--xberg-ocr-backend` (new, PR-A) selects `paddle-ocr`, which runs PP-OCRv6 small
  in-process. On generated 300 dpi fixtures it reads Chinese at CER **0.000** and
  English at **0.000**, where Tesseract is 0.075 (right `chi_sim`) or 0.453 (wrong
  `eng`). It costs ~3x the latency (1 252 ms vs 406 ms per 2-page scan) and ~3.4x
  the memory (765 MiB vs 224 MiB).
* Tesseract stays the default because it is exact on English, 3x cheaper, and
  changing a default changes every existing result.

**Evidence.** `docs/OCR-GPU-BENCHMARK.md` §2, `benchmark/results/ocr-cpu.json`.

**Rejected alternatives.** Making `paddle-ocr` the default (no benchmark on a real
gold set yet); a standalone PaddleOCR worker (duplicates an engine that is already
inside xberg); PP-OCRv6 medium (13x slower, identical CER).

---

## ADR-017 — No GPU execution provider in the worker until a faster GPU proves it

**Decision (2026-10-31, spike).** Do not add `--docling-rs-ep`, do not ship a CUDA
xberg build, and do not claim GPU acceleration for MinerU Flash.

**Evidence.**
* `ghcr.io/docling-project/docling-rs-cuda` (2.55.4) on the RTX 2080 Ti holds
  4.2 GB and takes **42 s** for a 2-page scan against 2.5 s on CPU, at ≤ 8 %
  utilisation — provider initialised, per-node CPU fallback. Its output is
  **byte-identical** to `EP=cpu, FP32=1`, i.e. CUDA also switches int8 -> fp32 and
  changes results.
* xberg 1.3.10's release build is compiled without CUDA and fails loudly when
  asked (`rebuild with the 'cuda' feature`); no CUDA asset exists upstream.
* MinerU Flash, run through the GPU worker's own entrypoint, showed **0 % GPU
  utilisation and no VRAM delta**, with identical latency for
  `MINERU_LMDEPLOY_DEVICE=cpu|cuda`. The PR-08 claim (~880 MB VRAM, 876 ms) is
  marked unverified.

**Consequence.** `device=gpu_required` keeps its promise only because the scheduler
enforces it; the engines do not. A follow-up PR adds a stderr guard that turns
"built without that provider / using CPU" into a failed attempt. Re-take this
decision on Ampere+ hardware with a 30-page document.

---

## ADR-018 — OCR geometry and confidence are not mapped into Document IR yet

**Decision (2026-10-31, spike).** Add nothing to the IR. Document IR already has
`BBox{x0,y0,x1,y1,coord}`, `PageWidthPt/PageHeightPt` and `Element.Confidence`;
what it lacks is a backend that fills them per OCR element.

**Evidence.** xberg JSON for a scanned page exposes page-level
`ocr_confidence.score` only for Tesseract (0.85, `word_count=205`) and returns
`null`/`0` for paddle-ocr; there are no per-word or per-line boxes, and block bboxes
are in **raster pixels** (0..3507) while the page is reported in PDF points
(595 x 842). `internal/engines/xberg.go` labels table bboxes
`pdf_user_space_v1` unconditionally, which is unverified for scanned pages.

**Consequence.** No fabricated confidence, no fake precision. Two prerequisites
before revisiting: per-element output in the engine contract, and a scanned-*table*
fixture that pins the coordinate space.
