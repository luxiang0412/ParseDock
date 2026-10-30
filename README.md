# ParseDock

Self-hosted document parsing service: upload a document, get structured text with
provenance, released on your terms.

```
POST /v1/jobs  ──▶  queued ──▶ running ──▶ completed
                                              │
                         GET …/result ◀───────┤  (markdown | text | json IR)
                                              │
                         POST …/ack  ─────────┘  release storage when you are done
```

* **One Go binary, four roles** — `standalone | api | coordinator | worker`.
* **Stateless API replicas** — any replica can serve any job; no sticky sessions.
* **One stateful Coordinator** — SQLite (WAL) + a local file store. No Redis, no
  Postgres, no object storage, no message queue.
* **Explicit lifecycle** — ACK-driven release, TTL expiry, download leases,
  tombstones (`410 Gone`), fencing tokens for workers.
* **Pluggable engines** — the control plane never imports engine types; adapters
  return a document IR (`doc-ir/0.1`) and the service renders Markdown/Text/JSON.

Status: **Phase 0 and Phase 1 complete** — PR-00 … PR-12 are merged. The one-page
status, per-slice evidence and the open items (each with its actual blocker) are in
`docs/PHASE1-SUMMARY.md`; the per-PR detail is in `docs/IMPLEMENTATION-PLAN.md`.
Still open and *not* code problems: external distribution of the worker image is
**blocked** on licence item L1 (`libx265`, GPL-2.0), two-host cluster measurement needs a
second host, and branch protection needs a paid private repo (or a public one).

Contributing, or working here as an agent? Read `AGENTS.md` first — it has the
non-negotiables, the command that *is* CI, and the traps we already paid for.

## Quickstart (local, no Docker)

```bash
make build                                  # ./runtime/bin/docparse

export PARSEDOCK_INTERNAL_TOKEN=$(openssl rand -hex 24)
export PARSEDOCK_API_KEYS="$(openssl rand -hex 24):acme"

# standalone = API + coordinator + one worker with the mock engine
./runtime/bin/docparse --role standalone --engines mock \
  --listen 127.0.0.1:8080 --internal-listen 127.0.0.1:8081 \
  --data-dir ./runtime/data --work-dir ./runtime/work

curl -s -X POST http://127.0.0.1:8080/v1/jobs \
     -H "Authorization: Bearer $KEY" -F file=@document.pdf
curl -s -H "Authorization: Bearer $KEY" \
     "http://127.0.0.1:8080/v1/jobs/<id>/result?format=markdown"
```

With real engines (`native`, `xberg`, `docling-rs` — see `docs/ENVIRONMENT.md` for
how to install them):

```bash
./runtime/bin/docparse --role standalone \
  --engines native,xberg,docling-rs \
  --xberg-bin ./runtime/engines/xberg-cli/xberg \
  --xberg-lib-dir ./runtime/engines/xberg-cli \
  --docling-rs-bin ./runtime/engines/docling-rs/docling-rs \
  --docling-rs-models ./runtime/engines/docling-rs
```

`./runtime/bin/docparse --check` prints host facts, the effective config and the
engine routing table as JSON.

## Quickstart (Docker Compose, CPU only)

```bash
make images                       # parsedock:0.1.0 + parsedock-worker-cpu:0.1.0
cp deploy/.env.example deploy/.env
docker compose -f deploy/compose.cpu.yaml --env-file deploy/.env up -d
# api-1 on :8080, api-2 on :8082, coordinator + worker on a private network
```

GPU (optional, MinerU flash): `docs/GPU-REPORT.md` and `deploy/compose.gpu.yaml`.

## API at a glance

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/v1/jobs` | upload + enqueue (multipart, `Idempotency-Key`, `mode`, `device`) |
| GET | `/v1/jobs/{id}` | status, retention state, result URLs, engine used |
| GET | `/v1/jobs/{id}/result?format=markdown\|text\|json` | streamed result, `ETag`/`X-Result-Version` |
| POST | `/v1/jobs/{id}/ack` | release the result (`202`, asynchronous deletion) |
| POST | `/v1/jobs/{id}/cancel` | stop a job / release a result |
| GET | `/v1/jobs/{id}/events` | SSE, resumable via `Last-Event-ID` |
| POST | `/v1/parse` | bounded synchronous parse (small documents) |
| GET | `/healthz`, `/readyz` | probes |

Full contract: `api/openapi.yaml` (validated with OpenAPI Generator; Python and Java
clients generated in `sdk/`).

```bash
# JSON IR, with page/bbox/table provenance
curl -H "Authorization: Bearer $KEY" "…/v1/jobs/$JOB/result?format=json" | jq '.elements[0]'
```

**Never auto-ACK.** A `200` on a download means the bytes left the server, not that
the consumer stored them. The Python SDK's `download_and_ack()` releases only after
your sink function returns successfully.

Semantics that are easy to get wrong:

* `POST /ack` and `POST /cancel` on a **completed** job are the same release
  operation and both require `result_version` (cancel returns `409` without it).
* `GET …/result` sends a per-representation `ETag` (validates those bytes) and
  `X-Result-Version` (the manifest hash, the only ACK token). `If-None-Match` → 304.
* `POST /v1/parse` is **one-shot**: `200` releases the result immediately; a timeout
  returns `202` and the job continues through the normal ACK/TTL path.
* SSE `scope=processing` (default) ends at the terminal processing state;
  `scope=retention` follows the job until `released`/`expired`.
* `mode=accurate` means "prefer structure and provenance", not "more accurate text
  everywhere"; text formats always use the native engine. See
  `docs/ENGINE-BENCHMARK.md` §7 for the measured counter-examples.

## Architecture

```
api replicas (stateless, auth + rate limit + reverse proxy)
        │  internal token + owner header
        ▼
coordinator (SQLite WAL, inputs/, results/, cleaner, leases, fencing)
        ▲  pull protocol: claim → input → heartbeat → result/fail
        │
workers (cpu: native/xberg/docling-rs · gpu: mineru flash)
```

Details and rationale: `docs/ARCHITECTURE.md`, `docs/CLUSTER-LITE.md`,
`docs/DECISIONS.md` (ADR log).

## Repository layout

```
cmd/docparse/            one binary, four roles
internal/api/            public gateway (auth, rate limit, reverse proxy)
internal/coordinator/    SQLite schema, job service, cleaner, internal/worker API
internal/jobs/           job model, statuses, error codes
internal/storage/        atomic file store, manifests, untrusted-input validation
internal/worker/         claim/heartbeat/result loop
internal/engines/        native, mock, xberg, docling-rs, docling-serve, tika, mineru
internal/documentir/     doc-ir/0.1 types + Markdown/Text renderers
migrations/              embedded SQL (single source of truth)
api/openapi.yaml         public + internal contract
deploy/                  compose.cpu / compose.gpu / compose.cluster-lite
docker/                  control-plane and engine worker images
benchmark/               fixture generator, bench harness, raw results, summary CSV
tests/integration/       T01–T16 acceptance harness
sdk/                     generated Python/Java clients + hand-written Python wrapper
docs/                    environment, licences, engines, IR, lifecycle, deployment…
```

## Verification

```bash
make test              # go vet + Go unit/integration tests + scoring unit tests
make contract          # OpenAPI (public+internal) / generated SDK / router drift
make pr00             # PR-00 runtime contract (10 scenarios) + backup/restore
make acceptance        # T03–T16 lifecycle (mock engine): ACK/TTL/lease/fencing/restart
make acceptance-real   # T01/T02/T11 against xberg + docling-rs
make sdk-test          # Python SDK lifecycle incl. "failing sink does not release"
make sdk-check         # Python + CLI + Java SDK build + one version across all artefacts
make metrics           # Prometheus exposition, throttling visibility, no content leakage
make load              # cluster-lite load report (mock engine, 2 api + 2 workers)
make gpu-check         # GPU worker image end-to-end (needs a GPU host)
make bench             # quality + latency + concurrency (needs engine servers)
make score             # regenerate the scoring tables from the raw data
make gate              # fail on >2 % quality or >15 % latency regression vs baseline
make drill             # online backup + restore drill, prints RTO
make fixtures          # regenerate synthetic fixtures + the 110-document corpus
make gpu-bench         # CPU vs CUDA, VRAM measurements
make license-audit     # SBOM + GPL gate for the built images
make ci                # run the CI phases locally (same commands as GitHub Actions)
make verify-pins       # go.mod / Dockerfiles / workflows vs images.lock.json
make lint-workflows    # actionlint on .github/workflows
```

Latest results (2026-10-30, this host): **13/13 lifecycle cases PASS**,
**3/3 real-engine cases PASS**. Raw evidence in `benchmark/results/raw/`,
summaries in `benchmark/results/summary/`.

## Engines (Phase 0 decision)

| mode | engine | measured (CPU, this host) |
| --- | --- | --- |
| `fast` | xberg 1.3.10 | 10–25 ms native PDF/DOCX/XLSX, 220–700 ms with OCR, 39 MB RSS |
| `accurate` | docling-rs 2.53.3 | 0.007 CER (EN) / 0.034 (ZH), full bbox + table spans, 0.8 GB RSS |
| text | native (Go) | < 1 ms, correct GB18030/UTF-16 decoding |
| fallback | Apache Tika 4.2.0 | broad coverage, text only |
| optional GPU | MinerU 4.1.2 `flash` | local-only GPU OCR; other tiers would upload to a third party |

Licences, including the `libx265` question in the xberg tarball and MinerU's
additional terms, are in `docs/LICENSE-REVIEW.md` — read it before distributing a
binary.

## Security posture (what exists today)

Bearer auth → owner; per-owner rate limit and upload caps; extension + magic +
OOXML structure + zip-ratio + PDF page checks before any engine runs; server-side
file names; per-job authorization for downloads/ACK; owner isolation returns `404`
for foreign jobs; engine subprocesses with timeouts, process-group kill and optional
network-namespace isolation that **fails closed**; no URL ingestion, no webhooks, no
document content in logs. Phase 1 added mTLS on the internal hop, per-owner storage quotas,
metrics/SLOs, an installable Python SDK with CLI, a 110-document benchmark corpus with a
quality gate, and a runbook with a measured restore drill. Gaps that remain are listed with
reasons in `docs/PHASE1-SUMMARY.md` (L1 `libx265` licence, two-host measurement, branch
protection on the free plan).

## Documentation

| Document | Contents |
| --- | --- |
| `AGENTS.md` | operational contract for contributors/agents: rules, commands, traps, deliberate gaps |
| `docs/PHASE0-SUMMARY.md` | Phase 0 final report (results, risks, next PRs) |
| `docs/PHASE1-READINESS.md` | PR-00 result: per-finding fixes, evidence, GO/NO-GO |
| `docs/PHASE1-SUMMARY.md` | Phase 1 status: what shipped, what was verified, what is blocked and why |
| `docs/SLO.md` | SLO targets, result-loss budget, metric reference, tracing stance |
| `docs/RUNBOOK.md` | backup/restore, upgrade, and one playbook per alert |
| `docs/SDK.md` | Python package/CLI, Java client, version rule, lifecycle contract |
| `docs/CI.md` | CI workflows, phases, pinned inputs, artifacts, licence policy |
| `docs/REAL-CORPUS-TEST.md` | 958-file real corpus: results, defects found, engine comparison |
| `docs/PR00-ISSUE-MATRIX.md` | F01–F11 verification against the code |
| `docs/ENVIRONMENT.md` | host, toolchain, engine versions, isolation |
| `docs/LICENSE-REVIEW.md` | per-component licence verification + open items |
| `docs/ENGINE-CANDIDATES.md` | what each candidate does/fails at |
| `docs/ENGINE-BENCHMARK.md` | corpus, quality/latency/RSS/throughput, method |
| `docs/ENGINE-DECISION.md` | weighted scoring, selected pair, risks, exit rules |
| `docs/ARCHITECTURE.md` | topology, request flow, package map |
| `docs/DOCUMENT-IR.md` | `doc-ir/0.1`, mapping per engine, gaps |
| `docs/JOB-LIFECYCLE.md` | states, timers, ACK semantics, cleanup algorithm |
| `docs/CLUSTER-LITE.md` | worker protocol, fencing, failure matrix, upgrade triggers |
| `docs/GPU-REPORT.md` | GPU verification and what remains |
| `docs/DEPLOYMENT.md` | images, compose, flags, probes, backups |
| `docs/DECISIONS.md` | ADR log |
| `docs/IMPLEMENTATION-PLAN.md` | Phase 1 PR slices |

## License

ParseDock code: Apache-2.0 (see `docs/LICENSE-REVIEW.md` §6). Third-party engines and
models keep their own licences; several require action before redistribution.
