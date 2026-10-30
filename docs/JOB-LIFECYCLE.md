# JOB LIFECYCLE — states, files, guarantees, evidence

> Implementation: `internal/coordinator/{service,cleanup,http}.go`,
> `internal/storage/store.go`, schema in `migrations/0001_init.sql`.
> Evidence: `benchmark/results/raw/acceptance-lifecycle-*.json` (T03–T16)
> and `acceptance-real-*.json` (T01/T02/T11).

## 1. Two dimensions, never one column

**Processing** — where the work is:

```
queued ──claim──▶ running ──result──▶ completed
   ▲                 │  │
   │   retryable     │  └──fail(non-retryable)──▶ failed
   └──backoff────────┘
                     └──max attempts / lease expiry──▶ failed
   queued ──────────cancel──────────▶ cancelled
   running ─────────cancel──────────▶ cancelled
```

**Retention** — whether the artifact still exists:

```
active ──ack──────────▶ release_pending ─┐
   │                                     ├─▶ deleting ─▶ released
   ├──TTL (completed)─▶ expiry_pending ──┘        │
   │                                              └─▶ expired
   └──failed/cancelled retention (10 min)─▶ release_pending
```

`released`/`expired` rows survive for `tombstone_ttl` (default 10 min) so clients
get a **410 Gone** instead of an indistinguishable 404, then the row is purged.

Invariants:

* `retention_status` is only advanced by compare-and-set updates
  (`WHERE retention_status IN (...)`); a lost race is a no-op, never a second delete.
* Files are deleted only after the row is in `deleting`, and a crash in `deleting`
  is retried on the next pass — file deletion is idempotent (`os.RemoveAll`).
* A job may not be marked `completed` without a committed manifest on disk, and
  files are never published without a later DB commit (orphan dirs are swept).

## 2. Timers (all absolute UTC, never "age of upload")

| Timer | Default | Starts | Enforced by |
| --- | --- | --- | --- |
| `processing_deadline_at` (`max_lifetime_at` = deprecated alias) | created + 24 h | upload | claim filter, engine deadline, cleaner — **queued/running only** |
| `result_expires_at` | commit + `result_ttl` (client may request up to `max_result_ttl`) | **result commit**, not upload | cleaner |
| failed retention | `updated_at + 10 min` | terminal failure | cleaner |
| download lease | 60 s, renewed every 20 s while streaming | each `GET …/result` | cleaner (skips busy jobs) |
| worker lease | 60 s, renewed every 10 s | claim | reaper |
| tombstone | 10 min | released/expired | cleaner |

Result TTL measured from commit matters: a 20-minute parse must not produce a
result that is already expired when it appears. PR-00 renamed the API field to
`processing_deadline_at` because it never bounded a completed result; the old name
is still returned as a deprecated alias (F03).

## 3. Submit → parse → download (defaults, measured)

```
POST /v1/jobs          202, ~10–60 ms (validate + fsync + insert)
claim + parse          engine dependent: native <1 ms, xberg 27 ms,
                       docling-rs ~800 ms/3 pages, MinerU ~0.9–1.7 s
GET …/result           streamed; X-Result-Version + ETag, Content-Length set
POST …/ack            202 immediately, deletion asynchronous
410 Gone               immediately after ACK for new downloads
files gone             within one cleanup interval (measured ≤ 2 s with 1 s interval)
```

## 4. ACK contract

Request:

```http
POST /v1/jobs/{id}/ack
Authorization: Bearer …
Idempotency-Key: ack-client-op-01
Content-Type: application/json

{"result_version":"sha256:…","action":"release"}
```

* `result_version` is mandatory for the first ACK and must equal the current
  manifest version; a mismatch is `409 conflict` and releases nothing.
* **Cancel does not bypass this.** `POST /cancel` on an already-completed job is a
  release, so it requires the same `result_version`; without it the call returns
  `409` and nothing changes (PR-00 / F03). Cancel remains a cancellation for
  `queued`/`running` jobs only.
* Response `202` with `retention_status=release_pending` means **intent accepted**,
  not "deleted". Poll `GET /v1/jobs/{id}` for `released`.
* Repeat ACK → `202` again, no state change, no other job touched (T06).
* ACK after `released`/`expired` → `202` (idempotent).
* ACK while a download lease is live → accepted, new downloads refused (`410`),
  files survive until the lease ends (T05: 8 MiB download held open, ACK returned
  202, the cleaner reported `skipped_active_downloads`, files were present during
  the stream and gone after the 5 s lease + cleanup).
* ACK racing TTL → exactly one terminal retention state, no orphan files (T07 ran
  4 races; all ended `released` or `expired` with zero files).
* ACK racing a **new download** → the lease insert is one conditional SQL statement
  against `retention_status='active'`, and the cleaner's `deleting` transition is a
  single `UPDATE … AND NOT EXISTS(live lease)`. There is no check-then-write window
  (PR-00 / F03, `TestDownloadLeaseIsAtomicWithRetention`).

Why no automatic ACK: an HTTP 200 means the bytes left the server, not that the
client consumed them. SDKs expose `ack()` and an opt-in `download_and_ack()` that
only fires after the caller's own sink reports success.

### Cancellation

`POST /v1/jobs/{id}/cancel` on a `queued`/`running` job:

1. the job becomes `cancelled` immediately and `cancel_requested_at` is recorded
   (visible to clients and operators before the worker has stopped);
2. the attempt row is marked `cancelled`, so any later heartbeat returns
   `409 cancelled` and any later result commit fails the fencing check;
3. the worker aborts the engine process at its next heartbeat (default 10 s;
   `--heartbeat`), then calls `/fail`, which is an idempotent no-op for a cancelled
   attempt — a cancelled job is never turned into `failed`;
4. if the engine had already produced a result, the worker discards it instead of
   uploading (PR-03): a cancelled job cannot publish a result that would race the
   cleanup;
5. input and partial work are removed by the retention path.

Measured: cancellation is observed and the worker stopped **within one heartbeat
interval** (T17 asserts < 12 s with a 1 s test interval; the mock engine runs 40 s
so the job is genuinely mid-parse). Per-engine ceilings (`--xberg-timeout`,
`--docling-rs-timeout`, `--mineru-timeout`, default 10/15/30 min) bound a single
engine invocation and are capped by `--max-parse-minutes` and the job's
`processing_deadline_at`.

## 5. Failure matrix (§4.3 of the task guide) and how it is covered

| Scenario | Behaviour | Evidence |
| --- | --- | --- |
| 200 received, app not done processing | no auto ACK; result stays until TTL | T04 |
| ACK then network loss | retry ACK; idempotent | T06 |
| ACK during download | new downloads 410; lease protects the stream | T05 |
| ACK and TTL together | single final state via compare-and-set | T07 |
| repeated download before ACK | allowed (each gets a lease) | T03/T05 |
| repeated ACK | 202, idempotent | T06 |
| Coordinator `SIGKILL` during cleanup | restart resumes `deleting`, files removed | T08 (3 jobs, 6 files left at kill, all gone after restart) |
| Worker `SIGKILL` mid-task | lease expires, job requeued, attempt 2 succeeds; stale token rejected | T09 (attempts: expired → succeeded; stale result/heartbeat `409`) |
| Client cancels a **running** job | attempt marked `cancelled`; the worker's next heartbeat returns `409 cancelled`, it aborts the engine and calls `/fail`, which is an idempotent no-op; the job stays `cancelled`; a result produced after the cancel is discarded | `TestCancelledAttemptFinalisation`, `TestCancelRecordsRequestTime`, T17 |
| Cancel a job whose engine already finished | the worker discards the result; no artifact is published | PR-03 worker guard, T17 |
| No GPU and `device=gpu_required` | `409 gpu_unavailable`, **no job row created** | T12 |
| Invalid/adversarial upload | `4xx` with stable code, service stays healthy | T13 |
| Disk below watermark | new upload `507`, existing results still downloadable | T14 |
| Late result from a superseded attempt | `409 lease_lost`, files deleted, no state change | T15 |
| Released/expired result | `410 gone` during tombstone, foreign owner gets `404`/`401` | T16 |

## 6. Cleanup algorithm (idempotent, crash-safe)

```
every cleanup_interval:
 1  reap worker leases: attempts(state=running, lease_until<=now)
      → attempt.state=expired
      → job: requeue with exponential backoff if attempts<max, else failed
 2  completed & result_expires_at<=now & active      → expiry_pending
 3  failed/cancelled & active & older than retention → release_pending
 4  for each pending job:
      live download lease? → skip (counted as skipped_active_downloads)
      mark deleting → delete inputs/ + results/ dirs → released|expired
      set tombstone_expires_at → emit event
 5  rows released|expired with tombstone<=now        → DELETE (cascades events/attempts/artifacts)
 6  delete expired download leases
 7  queued|running with max_lifetime_at<=now         → failed(timeout) + release_pending
 8  remove *.part older than 1 h, remove orphan input/result dirs older than 10 min
```

Steps 2–3 and 4 are separated on purpose: the state flip is a single SQL statement,
while the file deletion retries until it succeeds. A `SIGKILL` anywhere yields either
"still pending" or "already deleted", both of which converge.

## 7. Storage layout and atomicity

```
data/
  coordinator.db            SQLite WAL (local disk only)
  inputs/<job>/input.bin    published by rename from tmp/<job>.input.part
  inputs/<job>/meta.json    original name, client mime, owner (display/debug only)
  results/<job>/result.md|.txt|.json
  results/<job>/manifest.json  {result_version, files[{name,size,sha256}], engine, created_at}
  tmp/                      *.part staging (swept hourly)
```

`result_version = "sha256:" + H("name:size:sha256\n" for each file, sorted)`.
It is the value clients echo in ACK and is returned as `X-Result-Version`.

**ETag is a different thing (PR-00 / F05).** `ETag` is the strong validator of the
requested representation: `"sha256:<artifact sha256>"` for `result.md`,
`result.txt` or `result.json`. The manifest hash cannot validate one representation
(the files differ), so reusing it as an ETag would let a cache answer `304` for the
wrong body. `If-None-Match` is honoured; `Vary: Accept-Encoding` is set so a future
gzip/br encoder cannot poison caches. The ACK never accepts an ETag.

## 8. Disk watermark behaviour

`min-free-gb` (default 2 GiB) is enforced before an upload is accepted
(`507 insufficient_storage`, retryable). ACK/read paths keep working below the
watermark — that is how space is reclaimed. T14 verified: with the watermark set
impossibly high, upload was `507` while an existing result still downloaded with
HTTP 200.

## 9. What is deliberately not implemented

* Reference counting / multiple consumers per result (single logical result owner).
* Chunked/resumable uploads (would need a different contract, not an object store).
* Server-side webhooks (`P1`; would need HMAC + SSRF controls).
* Multiple representations sharing one ETag (they must not, see above).
* Automatic ACK anywhere in the server.
