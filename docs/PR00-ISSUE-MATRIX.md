# PR-00 ISSUE MATRIX — verification of the Phase 1 readiness review

> Reviewed 2026-10-30 against commit `57a08b1` (Phase 0 baseline) plus the PR-00
> changes in this branch. Every "verified" row cites the file/line that was read,
> a test that reproduces it, or a command whose output is quoted. Rows the review
> got wrong are marked **REJECTED** with the counter-evidence.

| ID | Verdict | Blocking? |
| --- | --- | --- |
| F01 Scoring arithmetic / comparability | **CONFIRMED** | yes (decisions quoted wrong numbers) |
| F02 `accurate` ≠ universally more accurate; CSV routing | **CONFIRMED** | yes |
| F03 ACK/Cancel/Sync/TTL state machine | **CONFIRMED** | yes |
| F04 Owner identity vs key rotation, internal boundary | **PARTIAL** (API sanitises headers; no rotation test; no listen-address guard) | yes |
| F05 ETag vs ACK version | **CONFIRMED** | no (P1, but must land before SDK release) |
| F06 OpenAPI 3.1 nullable / public-private split / multipart order | **CONFIRMED** | yes (before SDK release) |
| F07 SSE scope, replay, internal spec drift | **CONFIRMED** | no (P1) |
| F08 IR capability boundaries (image_ref, formula, bbox dims, Tika pages) | **CONFIRMED**, plus one defect the review missed (Tika page numbers were fabricated) | no |
| F09 Worker scratch retention | **PARTIAL** (per-attempt cleanup exists; no sweep/startup recovery) | no |
| F10 SLO/backup wording vs ACK semantics | **CONFIRMED** | no |
| F11 Licence gate / `libx265` | **CONFIRMED, BLOCKED on legal** | yes, for external distribution only |

---

## F01 — Scoring arithmetic and comparability

**What the review said.** `ENGINE-DECISION.md` quotes 7.4/6.5/5.1/8.1 but the
weights applied to the published sub-scores give 7.21/6.31/5.04/8.145; the CER mean
for docling-rs lists 7 values but the denominator/weights do not account for the
double-weighted scans; samples differ per engine so scores are not comparable.

**Verified.** All three claims are correct, and the underlying data was worse than
the review could see:

1. `benchmark/scripts/bench.py::table_accuracy` (Phase 0) averaged **only samples
   that produced a table**, so an engine that emitted no table at all scored 10.0
   on structure (Tika). Missing data was rewarded.
2. `has_bbox` was computed with `'"bbox"' in json.dumps(ir)`, which is true for
   xberg only when a table happens to carry `bounding_box`, and false for
   docling-serve even though its `json_content` has `prov[].bbox`.
3. The xberg adapter reported `ir["sheets"] = [page_number …]`, i.e. **page numbers
   were counted as sheet names** in the provenance metric (fabricated provenance in
   the *measurement*, not in the IR).
4. `native` was scored against PDF samples it cannot read (0.0 quality), and
   `resources` for engines without an RSS measurement was filled with `0.0`,
   silently penalising Tika/docling-serve.

**Fix.** `benchmark/scripts/score.py` is now the single source of truth:
fixed per-domain sample sets, applicability instead of zeros, per-sample provenance
expectations, table scored on every fixture that has one (missing → 0), CER clamped
to [0,1], scans double-weighted, unmeasured dimensions `null` + renormalised weights
+ `incomplete` flag. `docs/ENGINE-BENCHMARK.md` §7 and `docs/ENGINE-DECISION.md` §2
are generated from its output. Unit tests: `tests/unit/test_score.py`.

**Compatibility.** No API change. Decision text changes; the routing decision
(two engines) is unchanged and now justified by the corrected numbers.

---

## F02 — `accurate` is a policy preference, not a quality promise

**Verified.** `internal/engines/engine.go` `supportsMode` + `internal/coordinator/service.go`
`route()`: an explicit `mode=accurate` requires an engine that *declares* `accurate`.
`native` declares only `fast`, so **`mode=accurate` on CSV/TSV/TXT/MD routed to
xberg** — which is exactly the engine with the GB18030 defect. Measured:
`xberg csv_gb18030 CER 0.147` vs `native 0.000`.

**Fix.**
* `route()` gains an explicit text-format rule: `txt/md/csv/tsv` always prefer the
  `native` engine, for every mode, with `routing_reason` saying so.
* Documents that `fast`/`accurate` express *preference for structure and
  provenance*, not a per-document accuracy guarantee, with measured counter-examples
  (docling-rs scan EN CER 0.884 vs xberg 0.055; docx 0.284 vs 0.082).
* `routing.reason` + a warning record when a non-declaring engine serves an
  `accurate` request (text formats) and when OCR is used with unknown quality.
* Table-driven test `TestRouteTable` covering format × mode × device, plus a
  GB18030 end-to-end test that asserts the native engine serves all three modes.

**Compatibility.** Behaviour change: `mode=accurate` on text formats now returns
native output (same IR shape, better correctness). Documented in OpenAPI + docs.

**Not claimed.** Any language-specific hard routing from 24 synthetic files. The
sample is too small; that stays a PR-11 decision.

---

## F03 — ACK/Cancel/Sync/TTL state machine

**Verified defects (all reproduced by unit tests now in the repo):**

| Defect | Evidence |
| --- | --- |
| `POST /v1/jobs/{id}/cancel` on a **completed** job moves it to `release_pending` **without** `result_version`, bypassing the ACK guard | `internal/coordinator/service.go` `Cancel()` `case jobs.StatusCompleted` |
| A worker that is cancelled can neither heartbeat nor report failure: `Cancel` sets `attempts.state='cancelled'`, `Heartbeat` only matches `state='running'` → returns `lease_lost`; `Fail` calls `checkLease` → `409`. The job still ends `cancelled`, but the worker error path is unreachable and logs a false failure | `Heartbeat()` UPDATE … `AND state='running'`; `checkLease()` |
| `BeginDownload` checks `ResultAvailable()` and then inserts the lease in a **separate** statement — the cleaner can flip the job to `deleting` in between, creating a lease for a job being deleted | `BeginDownload()` |
| `max_lifetime_at` is documented as an "absolute maximum lifetime" but only bounds queued/running; completed results are bounded by `result_expires_at` alone | `cleanup.go` §6, `Claim()` filter |

**Fix.**
* Cancel on a completed job requires `result_version` (409 otherwise); queued/running
  cancel keeps working. Completed job without a version → `409 conflict` with an
  explicit message pointing at `/ack`.
* `Heartbeat` looks up the attempt by token regardless of state and returns
  `cancelled` (code `cancelled`) when the attempt was cancelled; `Fail` becomes
  idempotent for a cancelled/terminal job (returns the job, no error). The worker
  treats `cancelled` as a clean stop.
* `BeginDownload` is one `INSERT … SELECT … WHERE processing_status='completed' AND
  retention_status='active'`; the cleaner's `deleting` transition is one conditional
  `UPDATE … AND NOT EXISTS (active lease)`. No window between check and write.
* API exposes `processing_deadline_at` (canonical) and keeps `max_lifetime_at` as a
  deprecated alias with identical value; docs state it does **not** bound results.
* `/v1/parse` is documented and tested as one-shot: 200 = released, 202 = continues
  asynchronously, disconnect = the job keeps running (no implicit release).

**Compatibility.** Cancel on completed without a version now returns 409 instead of
202 — an intentional tightening; no shipped client depends on it (SDK always ACKs
with a version). New field is additive.

---

## F04 — Owner identity vs key rotation, internal boundary

**Verified, partially rejected.** The API gateway *does* strip client-supplied
identity headers (`internal/api/server.go` `Rewrite`: `Del("X-Internal-Token")`,
`Del("X-Parsedock-Owner")`, then sets its own) and the public mux has no `/internal`
route (`Handler()` only registers `/v1/` and probes; everything else 404s). So the
"external caller forges an internal header through the API" path claimed by the
review **does not exist**. What does exist:

* No test proves key rotation keeps `owner_id` stable, and no test proves the
  header-stripping.
* The Coordinator trusts `X-Parsedock-Owner` whenever it is reachable, and it starts
  with an **empty internal token** unless configured — a loopback-only deployment is
  fine, a bound-to-0.0.0.0 deployment without a token is not.

**Fix.** Owner mapping stays key→owner (rotation = two keys, same owner). Add
`validateInternalBinding()`: refuse to start when the internal listener is
non-loopback and no internal token is set (unless `--dev-insecure`). Tests:
rotation keeps access to the same job; injected headers are dropped at the API;
`/internal/*` is 404 at the API port.

**Compatibility.** Deployments binding the coordinator to a non-loopback address
now need `PARSEDOCK_INTERNAL_TOKEN` (they already needed it for correctness).

---

## F05 — ETag must describe the representation; `X-Result-Version` stays canonical

**Verified.** `handleResult` set `ETag: "<manifest version>"` for markdown, text and
json alike. The manifest hash covers all three files, so it is *not* a validator for
any single representation — a client revalidating a markdown body with it would get
a false 304 from a cache after only the JSON changed (and vice versa).

**Fix.** `ETag` = `"sha256:<artifact sha256>"` from the manifest entry for the
requested file; `X-Result-Version` remains the manifest hash and is the only value
the ACK accepts. `If-None-Match` returns `304` for the exact artifact. `Vary:
Accept-Encoding` is set now so a future gzip/br encoder cannot poison caches.
SDK already sends `X-Result-Version`; a test pins that.

**Compatibility.** ETag values change (they were wrong). `X-Result-Version` unchanged.

---

## F06 — OpenAPI 3.1 correctness, public/private split, multipart ordering

**Verified.** 8 `nullable: true` occurrences (3.0 syntax, invalid in 3.1);
`/internal/*` lives in the same document that generates the SDKs; the create handler
required the `file` part to arrive before scalar parts (it created the job while
streaming the file, so any later field was ignored).

**Fix.** `api/openapi.yaml` = public only (changed-endpoint semantics documented),
`api/openapi.internal.yaml` = worker protocol + diagnostics. All `nullable` replaced
with `type: [x, 'null']`. Both validate with OpenAPI Generator.
The create handler now accepts **any part order**: the file is streamed to the
staging area (`storage.Store.NewInputWriter`) while fields are collected, and the job
is created after the whole body is parsed. No full-file buffering (the writer streams
to `tmp/*.part`, bounded by `max_upload_mb`).

**Compatibility.** Purely additive for clients (previously working orders still
work). SDKs regenerate from the public spec only.

---

## F07 — SSE scope, replay window, internal spec drift

**Verified.** `handleEvents` sends `stream_end` when the *processing* status is
terminal, so `released`/`expired` (retention) events are never delivered — yet the
public spec documented them as part of the stream. `events` are deleted with the job
row (cascade), i.e. the replay window equals the tombstone window (10 min default),
and a cursor older than that silently returned nothing.

**Fix.** Explicit `scope=processing|retention` (default `processing`), documented:
`processing` ends at completed/failed/cancelled; `retention` continues until
`released`/`expired` or the stream times out (10 min). A cursor older than the
oldest retained event yields a fresh `snapshot` event (never a silent gap) plus a
`cursor_reset` event. `/internal/workers/{id}/heartbeat` and the diagnostics endpoint
are now in the internal spec.

**Compatibility.** Additive query parameter; default behaviour is unchanged but now
documented truthfully.

---

## F08 — IR capability boundaries

**Verified.**

* `image_ref` is defined but **never emitted** by any adapter (grep), so no dangling
  reference exists today; the docs implied images were referenced.
* **New defect the review did not catch:** the Tika adapter split pages on `\f`, but
  Tika 4.2 does not emit form feeds for the test PDF (`formfeeds 0` on a 3-page
  file), so a 3-page PDF was reported as one page and every element got page 1 —
  fabricated provenance. Verified with `curl … /tika` on the fixture.
* XLSX formula text is not available from xberg or docling-rs (measured), so
  `Cell.Formula` stays empty; this is a capability gap, not a bug.
* `SourceLocation` has no page dimensions, so a bbox cannot be rendered to pixels
  without a second lookup.

**Fix.** Tika adapter: only assign page numbers when form feeds actually appear;
otherwise omit `page` and add warning `no_page_boundaries`; take the page count from
`xmpTPg:NPages` when available. Tika OOXML output is markdown, so it goes through
`LiftMarkdown` (headings/tables recovered instead of flat paragraphs).
`SourceLocation.page_width_pt`/`page_height_pt` added and populated from Docling's
`pages` map for PDF/slide inputs only — for XLSX the same numbers are a cell-grid
extent, so reporting them as points would be fabricated provenance (a test pins
this). `image_ref` documented as reserved and asserted unused by a test.
Formula extraction is explicitly deferred to PR-05 (BLOCKED on engine capability).

**Compatibility.** Additive IR fields; Tika results lose a *wrong* page number and
gain a warning — a correctness fix.

---

## F09 — Worker scratch retention

**Verified.** `execute()` has `defer os.RemoveAll(workDir)` (so normal and error
paths clean up), but a `SIGKILL` leaves the directory forever, and nothing sweeps
`runtime/work` on start or while running.

**Fix.** Worker startup sweeps `<work-dir>/*/*` older than `--work-max-age`
(default 1 h); a background scrubber runs every `--work-scrub-interval` (default
10 min) and skips attempt directories whose job is currently leased by this worker.
Tests cover crash leftovers, live-attempt immunity, and the age threshold.

**Compatibility.** New flags with safe defaults.

---

## F10 — SLO and backup semantics

**Verified.** `IMPLEMENTATION-PLAN.md` PR-09 says "result-loss budget (must be zero
for acknowledged results)" — but ACK means *delete*, so the target is nonsensical;
PR-12 proposed backing up inputs/results without saying that a restore would
resurrect released or expired artifacts.

**Fix.** SLO reworded to "committed, not-yet-expired, not-yet-ACKed results must not
be lost inside the supported failure model (single host, local disk). Disk loss is a
single point of failure, explicitly outside the budget." Backup guidance: back up
SQLite + the *active* set only; a restore re-runs the cleaner and must not make a
released result downloadable. Implemented as `scripts/backup.sh` + a restore test.

---

## F11 — Licence gate

**Verified.** The xberg release tarball still contains `libx265.so.199`; the image
build copies it. No SBOM and no gate exist. CUDA redistribution for the GPU worker is
not reviewed.

**Fix (engineering half only).** `scripts/license_audit.sh` inspects built images
(dpkg inventory + bundled `.so` scan + xberg tarball marker) and writes
`build/sbom/<image>.json`; it **fails** when a GPL library is present unless
`APPROVED_GPL=1` is set with a documented reason, and it records the MinerU
attribution requirement. `make license-audit` wires it in.

**BLOCKED.** The legal decision (rebuild without libx265 vs. GPL source offer vs.
replacement engine) is **not** made here and must not be inferred from the process
boundary. External distribution of `parsedock-worker-cpu` remains blocked until L1
is signed off.
