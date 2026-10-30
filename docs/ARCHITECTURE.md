# ARCHITECTURE — ParseDock v0.1 (Phase 0)

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
* **worker** = stateless; all state is a lease it holds.

### Why the API is a reverse proxy

An earlier design had the API call coordinator RPCs per operation. A reverse
proxy is smaller, streams results/SSE without buffering, and makes "no sticky
sessions" trivially true. The coordinator therefore exposes the public `/v1`
routes on its *internal* listener, guarded by mTLS (optional, PR-07), the internal
token and an owner header injected by the gateway. The public port never reaches the coordinator.

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
| `internal/jobs` | job aggregate, statuses, error codes, ULID ids |
| `internal/storage` | atomic file store, manifests, disk watermark, magic/zip/PDF pre-flight |
| `internal/worker` | register/claim/heartbeat/input/result loop, engine selection, scratch dirs |
| `internal/engines` | adapter interface + native/mock/xberg/docling-rs/docling-serve/tika/mineru |
| `internal/documentir` | IR types + Markdown/Text renderers |
| `migrations` | embedded `*.sql` (single source of truth) |

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
| public → api | bearer token → owner; keys stored as SHA-256 with scopes (`jobs:write`/`read`/`ack`); per-owner token bucket; `MaxBytesReader`; `X-Content-Type-Options: nosniff`; validated `X-Request-Id` |
| api → coordinator | internal shared token; owner header stripped from client input |
| worker → coordinator | mTLS (PR-07) + internal token + per-task lease token; input/result authorised by token |
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
 "format":"pdf","bytes":4855,"request_id":"req_…","engine_hint":"xberg",
 "mode":"auto","device":"auto","time":"2026-10-30T12:00:00Z"}
```

Events: `job_created`, `job_acked` (both ACK and cancel-after-complete),
`job_cancelled`, `job_released`, `job_expired`. Fields are limited to identities,
ids, sizes, engine/mode and the decision — **never document text, filenames from
the client, tokens or result bodies**. `request_id` comes from the API gateway and
is generated/validated there (charset `[A-Za-z0-9._:-]`, ≤128 chars).

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
