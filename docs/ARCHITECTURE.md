# ARCHITECTURE — ParseDock v0.1

> Living document: describes the current code (v0.1.0 + the unreleased changes in
> `docs/CHANGELOG.md`). It started in Phase 0; dated reports are in `docs/history/`.

## 1. Goals that shape the design

1. **One binary, four roles.** `cmd/docparse --role standalone|api|coordinator|worker`.
   The control plane is Go; engines are external processes/services.
2. **Stateless API.** Any API replica can serve any job because replicas own no state
   and no files; they authenticate, rate-limit, cap uploads and reverse-proxy.
3. **One stateful component.** A single Coordinator owns SQLite (WAL) and the file store.
   Scaling is horizontal *in compute*, not multi-active HA (explicit non-goal for P1).
4. **No mandatory infrastructure.** Local disk + SQLite only. No Redis/Postgres/S3/MQ.
5. **Explicit lifecycle.** Two independent state dimensions (processing, retention),
   ACK-driven release, TTL expiry, download leases, tombstone reads.
6. **Fencing.** Lease tokens prevent a restarted or timed-out worker from publishing
   a stale result.
7. **Engine-agnostic IR.** No engine type leaks into the control plane; adapters return
   `doc-ir/0.1` and the Coordinator renders Markdown/Text/JSON from it.

## 2. Topology

```
                      ┌─────────────┐
   client ── HTTPS ──▶│  api-1      │──┐
                      └─────────────┘  │  internal token + X-Parsedock-Owner
                      ┌─────────────┐  │  (reverse proxy, streaming)
   client ── HTTPS ──▶│  api-2      │──┤
                      └─────────────┘  │
                                       ▼
                            ┌───────────────────────┐
                            │     Coordinator       │  SQLite WAL
                            │  /v1/*  (internal)    │  data/inputs, data/results
                            │  /internal/tasks/*    │  cleaner, leases, fencing
                            └─────────┬─────────────┘
                                      │ claim / heartbeat / input / result
                    ┌─────────────────┼─────────────────┐
                    ▼                 ▼                 ▼
              ┌──────────┐      ┌──────────┐      ┌──────────┐
              │ worker   │      │ worker   │      │ worker   │
              │ cpu      │      │ cpu      │      │ gpu      │
              │ xberg    │      │ docling  │      │ mineru   │
              └──────────┘      └──────────┘      └──────────┘
```

* **standalone** = coordinator + api + one worker in one process, two listeners
  (`--listen` public, `--internal-listen` coordinator), worker talking to the
  local coordinator over HTTP. This is deliberate: the standalone path uses the
  same code as the cluster path, so the acceptance tests exercise production
  code, not a special case.
* **api** = only the gateway (no DB handle, no file access).
* **coordinator** = only state and files; never runs engines.
* **worker** = stateless; all state is a lease it holds. Workers authenticate with the
  worker token (`--worker-token`), which opens only the worker protocol.
* **playground** (optional, `web/`, compose profile `playground`) = a static React app plus
  a same-origin `/v1` proxy (Vite dev/preview proxy or nginx) that injects the API key from
  the environment, so the browser never holds it. It is an ordinary API client of an `api`
  replica, binds `127.0.0.1` by default (`PLAYGROUND_BIND`), and never ACKs a result
  automatically. See `docs/PLAYGROUND-IMPLEMENTATION.md`.

### OCR escalation (worker-local)

With `mode=auto` the worker first runs the fast engine. If the result is a scannable format
and carries a `no_text_layer` warning or a measured OCR confidence below the threshold, the
worker retries it **once** on the accurate engine inside the same attempt
(`--escalate-scans`, default on). The retry replaces the fast result only if it succeeds,
is non-empty and does not itself report low confidence; either way the decision is recorded
in `routing.reason` (`internal/engines/escalate.go`, `internal/worker/worker.go`). The
coordinator is not involved: the escalated attempt holds the same lease.

### Why the API is a reverse proxy

An earlier design had the API call coordinator RPCs per operation. A reverse
proxy is smaller, streams results/SSE without buffering, and makes "no sticky
sessions" trivially true. The coordinator therefore exposes the public `/v1`
routes on its *internal* listener, guarded by mTLS (optional, PR-07), the internal
token and an owner header injected by the gateway. The public port never reaches the coordinator.

## 2a. Observability

The coordinator exposes Prometheus text metrics on `/internal/metrics` (internal token,
mTLS-aware). Database-derived series (jobs by state, queue depth, retention, workers online,
disk free) are computed **at scrape time**, so job transitions stay single-write and one
scrape always describes one consistent database state. API replicas are stateless and add no
series beyond the shared `parsedock_throttled_total` family, which is why the coordinator is
the only scrape target. See `docs/SLO.md`.

## 3. Request flow (async)

```
POST /v1/jobs (multipart, parts in any order)
  api:  auth → rate limit → MaxBytesReader → proxy
  coord: stream file part to tmp/*.part (bounded) while collecting fields
         → sniff/validate → fsync → rename → insert job(queued) → 202
  worker: POST /internal/tasks/claim   → attempt+lease_token, status=running
          GET /internal/tasks/{id}/input (lease token) → local copy + sha256 check
          engine.Parse(ctx) → IR
          PUT /internal/tasks/{id}/result (lease token) → coordinator renders md/txt/json,
              writes result.* atomically, commits DB with fencing check
GET /v1/jobs/{id}/result?format=markdown
  coord: one conditional INSERT creates the download lease only while retention is
         active → stream + renew + ETag/If-None-Match → delete lease
POST /v1/jobs/{id}/ack {result_version}
  coord: active → release_pending (idempotent), refuses new downloads
cleaner (every 60 s):
  release_pending/expiry_pending + no live lease → deleting → delete files → released/expired
  → tombstone for 10 min → row purged
```

## 4. Package layout

| Path | Responsibility |
| --- | --- |
| `cmd/docparse` | flags, role wiring, engine registry construction, `--check` capability dump |
| `internal/api` | public mux, bearer auth, per-owner token bucket, reverse proxy, body caps, header sanitisation |
| `internal/coordinator` | SQLite schema/migrations, job service, internal + proxy mux, cleaner |
| `internal/jobs` | job aggregate, statuses, error codes (`job.go`; the known-code set in `codes.go`), ULID ids |
| `internal/storage` | atomic file store, manifests, disk watermark, magic/zip/PDF pre-flight |
| `internal/worker` | register/claim/heartbeat/input/result loop, engine selection, OCR escalation (`maybeEscalate`), scratch dirs, VRAM reporting |
| `internal/engines` | adapter interface + native/mock/xberg/docling-rs/docling-serve/tika/mineru, escalation triggers (`escalate.go`), CLI runner + error classifier (`exec.go`) |
| `internal/documentir` | IR types + Markdown/Text renderers + embedded JSON Schema (mirrored to `api/`) |
| `internal/metrics` | dependency-free Prometheus exposition (no SDK dependency) |
| `internal/tlsutil` | server/client TLS configs for the internal hop (mTLS, timeouts preserved) |
| `internal/sysinfo` | host facts, GPU detection, per-device free VRAM |
| `migrations` | embedded `*.sql` (single source of truth) |
| `web/` | Playground (React + Vite, own `package.json`; not part of the Go build); `deploy/nginx.playground.conf.template` is its production proxy |

## 5. Concurrency and resource model

* SQLite: WAL, `busy_timeout=5000`, `synchronous=FULL`, short transactions, **no file or
  network I/O inside a transaction**. File writes happen before the commit; if the
  commit loses the fencing check the freshly written files are deleted.
* Worker concurrency is per-process (`--worker-concurrency`) and the number of workers
  is unbounded; the Coordinator serialises claims with one atomic `UPDATE … RETURNING`.
* Progress writes are throttled by the worker's heartbeat interval (default 10 s).
* Result downloads stream with `io.Copy` and renew the download lease every `ttl/3`.
* Engines run as subprocesses with process-group kill on timeout, a worker-owned scratch
  dir, `NO_COLOR`, and optional `unshare -n` network isolation (fails closed when the
  kernel/permissions do not allow it).

## 6. Security boundaries

| Boundary | Control |
| --- | --- |
| public → api | bearer token (or `X-Api-Key`) → owner; keys stored as SHA-256 with scopes (`jobs:write`/`read`/`ack`), plaintext keys ≥ 16 chars and one owner per token enforced at startup; failed authentication limited per client address before any owner is known; per-owner token bucket (per replica, bounded map); `MaxBytesReader` + per-request body read deadline; `X-Content-Type-Options: nosniff`; validated `X-Request-Id`; `X-Forwarded-For` believed only from `--trusted-proxies` |
| api → coordinator | internal (gateway) token — the only credential that may assert `X-Parsedock-Owner`; owner, client-IP and internal headers stripped from client input, the client's own key not forwarded; resolved client IP forwarded and believed only with the internal token; dedicated proxy transport (150 s header timeout > 120 s sync window) |
| worker → coordinator | mTLS (PR-07) + worker token (`--worker-token`; falls back to the internal token when unset) + per-task lease token; the worker token opens only `/internal/workers/*` and `/internal/tasks/*` and gets `403` on `/v1` and operator endpoints. Certificate roles are not bound to tokens |
| operator → coordinator | `/internal/metrics`, `/internal/diagnostics/*` and the detailed `/readyz` need the internal token; unauthenticated `/readyz` returns verdicts only (no free bytes, timestamps or error strings) |
| standalone | the in-process api→coordinator and worker→coordinator hops use random tokens generated at start when none are configured |
| playground | API key injected by the same-origin proxy from the environment (never `VITE_*`, CI fails if a key reaches `dist/`); listener bound to `127.0.0.1` unless `PLAYGROUND_BIND` says otherwise; CSP/`X-Frame-Options`/nosniff on `index.html` |
| upload | extension + magic bytes + OOXML structure + zip-bomb ratio + PDF markers + page limit; multipart parts may arrive in any order without buffering the document |
| representations | `ETag` validates one representation; `X-Result-Version` (manifest hash) is the only ACK token |
| filenames | server-generated job ids; original name kept as metadata only (`filepath.Base`, control chars stripped) |
| engine | subprocess, timeout, private scratch dir, optional netns; no shell interpolation |
| logs | no document text, no tokens, no result bodies; only ids/counters/error codes |
| cross-tenant | a foreign job id returns **404** with the same body as an unknown id |
| quotas | per owner, enforced by the coordinator (not the stateless API): concurrent jobs, jobs/day, bytes/day; `429` + `Retry-After`. Admission is serialised per owner and consumption is recorded in `owner_usage` in the same transaction as the job insert, so concurrent uploads cannot overshoot and tombstone cleanup cannot restore a spent budget |
| egress | no URL fetching, no webhooks, MinerU remote tiers disabled |

### Audit log

The coordinator writes one JSON object per decision to the process log with
`audit=true` (ship it separately from application logs):

```json
{"audit":true,"event":"job_created","owner_id":"acme","job_id":"job_01…",
 "format":"pdf","bytes":4855,"request_id":"req_…","client_ip":"203.0.113.9",
 "engine_hint":"xberg","mode":"auto","device":"auto","time":"2026-10-30T12:00:00Z"}
```

Events: `job_created`, `job_acked`, `job_cancelled` (queued/running only — cancelling
a completed job with an active result is refused with `409` and writes no audit line), `job_released`,
`job_expired`. Fields are limited to identities,
ids, sizes, engine/mode and the decision — **never document text, filenames from
the client, tokens or result bodies**. `request_id` comes from the API gateway and
is generated/validated there (charset `[A-Za-z0-9._:-]`, ≤128 chars). `client_ip` is the
address the gateway resolved (TCP peer, or `X-Forwarded-For` behind a configured trusted
proxy); it is present on client-initiated events and absent on cleanup/worker events.

Key handling: `--api-keys-file` (one `<secret|sha256:hash>:<owner>[:scopes]` per
line) is the production form; plaintext `--api-keys` is hashed at startup but
visible in argv, so it is for development only.

## 7. Non-goals restated

* No multi-active Coordinator, no Raft, no cross-host SQLite.
* No user/tenant management UI, billing, RAG, embeddings, vector store.
* No object storage, MQ, or external DB.
* No remote URL ingestion (future feature requires its own SSRF review).

## 8. Measured limits of this topology

* The Coordinator is the network and disk bottleneck: every input and result byte
  passes through it. Measured on this host: see `docs/ENGINE-BENCHMARK.md`
  (§ throughput) and `docs/CLUSTER-LITE.md` (§ upgrade triggers).
* One Coordinator means a single restart point; in-flight jobs survive because the
  state is durable and leases recover (verified by T08/T09), but the API is degraded
  while it restarts (measured recovery: < 5 s).
