# Playground readiness — current state, API surface, preview options, plan

> Read-only investigation of `main` at `f1fe2af` (2026-10-31). Every claim below cites the
> code, the OpenAPI document, or a command that was actually run. No backend, API or
> dependency was modified; nothing was started that outlived the check.

## 1. Project state

| Item | Value | Evidence |
| --- | --- | --- |
| Branch / HEAD | `main` @ `f1fe2af` (merge of PR #26) | `git log -1` |
| Worktree | clean (0 modified files) | `git status --porcelain` |
| Merged PRs | #3 (real-corpus defects), #4–#7 (PR-02…PR-05), #8 (PR-06), #9 (PR-07), #10 (PR-08), #11 (PR-09), #12 (PR-10), #13 (PR-11), #14 (PR-12), #15 (AGENTS.md), #16 (doc sync), #18–#23 (audit batches A–F), #24 (html + page cap), #25 (table dedupe), #26 (export tool) | `gh pr list --state all` |
| Open work | L1 licence (`libx265`) blocks worker-image **redistribution**; two-host cluster measurement; branch protection (free private repo); sheet-window extraction for 16 oversized XLSX (deferred by decision); zip-archive ingestion (declined) | `docs/PHASE1-SUMMARY.md`, `docs/history/AUDITS.md` |
| Backend stack | Go 1.27.3, single binary with 4 roles, SQLite (WAL) + local filesystem, no Redis/Postgres/S3/MQ | `go.mod`, `docs/ARCHITECTURE.md` |
| Frontend code | **none** — no `web/`, `frontend/`, `ui/`, no `package.json` | `ls`, `find -maxdepth 2 -name package.json` |
| Node toolchain available | Node 22.22.3, npm 10.9.8 | `node -v`, `npm -v` |
| Tests | 133 Go test funcs; 46 Python unit tests (score 21, gate 11, ci_summary 10, fixtures 4); acceptance T01–T17; contract 36 checks; runtime contracts PR-00 10, PR-02 6, PR-07 8, PR-09 5, PR-12 7 (counts observed by running them) | `grep -c "^func Test"`, `tests/unit/*`, `tests/integration/*` |
| CI | `ci.yml` (static, unit, contract, lifecycle, image, ci-summary), `engine-changes.yml`, `nightly.yml`, `release-gate.yml`; latest `main` runs green | `gh run list --branch main` |
| Docker | one control-plane image (`EXPOSE 8080 8081`), a CPU worker image, an optional GPU worker image | `docker/Dockerfile`, `deploy/compose.cpu.yaml`, `docker/Dockerfile.worker-gpu` |

Running components in the CPU stack (`deploy/compose.cpu.yaml`): `coordinator` (no published
port), two stateless `api` replicas (8080, 8082), one `worker-cpu` (`--worker-concurrency=2`,
scratch on tmpfs), networks `frontend`/`backend` with `backend` marked `internal: true`.

GPU: `docker/Dockerfile.worker-gpu` builds and is verified on the RTX 2080 Ti
(`tests/integration/pr08_gpu.py`, 4/4 — MinerU 4.1.2 on GPU, `gpu_required` completes), but the
image is **internal-only** because of L1.

## 2. HTTP API capability matrix

The browser must talk to the **api role** (published port), never the coordinator: the
coordinator's public mux expects `X-Internal-Token` + `X-Parsedock-Owner`
(`internal/coordinator/http.go`, `guard()`), while the api gateway authenticates the bearer key
and injects the owner (`internal/api/server.go`, `auth()`).

| # | Method + path | Request | Response | Works today | Playground use | Limits / notes |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `POST /v1/jobs` | `multipart/form-data`: `file` (required), `mode` `auto` / `fast` / `accurate`, `device` `auto` / `cpu` / `gpu_preferred` / `gpu_required`, `priority`, `ttl_seconds`, `idempotency_key`; same fields accepted as query params (query wins) | `202` + job view, `Location: /v1/jobs/{id}` | **yes** | upload | body capped at `--max-upload-mb`+8 MiB at the gateway (`handleV1`); parts may arrive in **any order** (F06); `415 unsupported_type` for a rejected type, `413 file_too_large`, `507 insufficient_storage`, `429 quota_exceeded`/`rate_limited` |
| 2 | `POST /v1/parse?timeout_ms=` | same upload | `200` **IR JSON only** + `X-Result-Version`, `X-Job-Id`; `202` + job view if the window (≤2 min) elapses; on failure the code's usual status (`422` document/engine, `429 quota_exceeded`, `409 cancelled`) | **yes** | **not for the playground** — it does not release the result (fixed in the 1.0 hardening: the job follows the TTL) (`handleSyncParse`: "Synchronous responses have no retention window") | no `format` choice, no explicit release, needs a ≥30 s timeout |
| 3 | `GET /v1/jobs/{id}` | – | job view: `status`/`processing_status`, `retention_status`, `progress_percent`, `progress_stage`, `engine`, `routing_reason`, `page_count`, `warning_count`, `error_code`/`error_message`, `result_version`, `result_expires_at`, `result_urls` per format, `*_url` helpers | **yes** | polling fallback, status strip | `result_version`/`result_urls` are `null` until a result exists; job of another owner → `404` |
| 4 | `GET /v1/jobs/{id}/events?scope=processing|retention` | `Last-Event-ID` header or `last_event_id` | `text/event-stream`: `snapshot` first, then `id:`+`event:`+`data:` per event, `cursor_reset` if the cursor is too old, `stream_end` | **yes** | live progress | server polls the DB every 500 ms and sends `: keepalive` every 15 s; the stream ends after 10 min (`sseMaxDuration`) with `stream_end{reason:max_duration}`; sets `X-Accel-Buffering: no` |
| 5 | `GET /v1/jobs/{id}/result?format=markdown|text|json` | `If-None-Match` (ETag) | `200` body, `text/markdown`, `text/plain`, `application/json` (all `charset=utf-8`), `ETag` (representation), `X-Result-Version` (manifest), `Vary: Accept-Encoding`, `Content-Disposition: attachment`, gzip when requested and >1 KiB; `304` when the ETag matches | **yes** | fetch markdown/IR | `409` while not completed, `410` after release/expiry (tombstone), `404` after the tombstone; a live download lease (60 s, renewed) protects the files |
| 6 | `POST /v1/jobs/{id}/ack` | `{"result_version":"sha256:…"}` or `If-Match` | `202` `{job_id, retention_status, result_version, accepted_at}` | **yes** | explicit "release server copy" | requires `processing_status=completed` **and** the exact non-empty `result_version` (`service.go` `Ack`, PR-22/A2); idempotent; `409` on a stale version, `400` if the version is missing |
| 7 | `POST /v1/jobs/{id}/cancel` | optional `{"result_version":…}` | `202` + job view | **yes** | "cancel" while queued/running | a *completed* job is not cancelled and is never released by cancel (`409`); use ACK |
| 8 | `GET /healthz`, `GET /readyz` | – | `{status:ok}` / readiness map (sqlite, storage, cleanup, workers) | **yes** | proxy health, "stack down" banner | on the api mux these are separate from `/v1` and need no key |
| 9 | Auth | `Authorization: Bearer <key>` or `X-Api-Key` | `401 unauthorized`, `403 insufficient_scope` | **yes** | via a proxy that injects the header | scopes: `jobs:write` (create), `jobs:read` (status/result/events), `jobs:ack` (ack/cancel) — `NeededScope()` in `internal/api/keys.go`; keys are SHA-256 hashed at start-up (`--api-keys-file` preferred) |
| 10 | `GET /internal/*` | `X-Internal-Token` | metrics, diagnostics | **yes**, but internal | not used by the UI | must not be exposed through the public proxy |

Not documented / not present, and needed or worth adding (see §7): a CORS layer, a
**capabilities** endpoint (accepted extensions per deployment), an input re-download endpoint,
and any static-file serving (`internal/api/server.go` `mux()` serves only `/healthz`, `/readyz`,
`/v1/`).

**Inconsistencies found** (small, worth fixing while building the UI):

1. The OpenAPI job view documents `status` as the processing status, and the handler also emits a
   redundant `processing_status`. Harmless, but the generated client will show both.
2. `/v1/parse` is documented as "bounded synchronous parse" but used to silently ACK the result (fixed); a
   client that wants the IR afterwards gets `410`. Either document that or add `no_ack`.
3. `warning_count` is the only warning surface on the job; the actual codes/messages live in
   `result.json` (`warnings[]`), so the UI needs one extra fetch for a warnings panel.
4. `result_urls` are relative paths (`/v1/jobs/…`), which is what the proxy needs; a UI that
   calls the API cross-origin must prefix them.

## 3. Original-file preview matrix

The API has **no endpoint that returns the uploaded file** (only the internal worker route
`GET /internal/tasks/{id}/input`). That is fine: the playground holds the `File` object and can
preview it locally with `URL.createObjectURL`, which is also faster and avoids a second upload.
Consequence: after a page reload the job is still parseable server-side but the original is gone
from the browser — the UI must say so and offer "upload again" (or a later input-download
endpoint, §7).

| Format | v1 approach | Library (licence) | Fidelity | Notes |
| --- | --- | --- | --- | --- |
| PDF | native browser viewer in an `<iframe src=blob:…>` | none (or `pdfjs-dist` Apache-2.0 later) | exact | Chrome/Firefox/Safari all render PDFs; blob URLs are same-origin. Page-level control (jump to page from the IR) needs pdf.js |
| PNG / JPEG / WebP | `<img src=blob:…>` | none | exact | zoom/pan = CSS or a small viewer |
| TXT / MD | `<pre>` from `blob.text()` | none | exact | prefer `TextDecoder` for non-UTF-8 (the corpus has GB18030 CSVs; `TextDecoder('gb18030')` is in the Encoding Standard) |
| CSV / TSV | read-only table | `papaparse` (MIT) | good | detect delimiter; cap rows for huge files |
| HTML / HTM, and `.doc` whose content is HTML | sandboxed `<iframe sandbox src=blob:…>` | none | good | must use `sandbox` **without** `allow-same-origin`; the server already detects this case (`htmlMagic`, `SniffFormat`) |
| DOCX | rendered HTML preview | `docx-preview` (MIT) | approximate | layout/tables/headers usually fine; no tracked changes, no fields evaluation |
| XLSX / XLSM | sheet tabs + read-only cell grid | `exceljs` (MIT) — or SheetJS CE (Apache-2.0) | approximate | formulas shown as text/cached value, no charts, no pivot tables; cap rows/cells in the preview (the API itself renders up to 10M cells) |
| PPTX | **skip in v1** | – | – | no mature lightweight renderer; show file info + "preview not available" |
| Legacy `.doc` / `.xls` (OLE2) | **skip in v1** | – | – | Word 97 binary format has no browser renderer; the *parse* result is still shown |
| Anything else | metadata card + explicit limitation | – | – | required behaviour, per the brief |

Risks for preview: memory (never read a 128 MiB file into a JS string; use Blob URLs and
`ArrayBuffer` only for the Office libraries, then revoke), sandboxing (HTML previews are
attacker-controlled), and fidelity expectations (Office previews are approximations — say so in
the UI, and never let the preview look authoritative next to the parsed output).

## 4. Markdown display

| Requirement | Recommendation |
| --- | --- |
| render | `react-markdown` (MIT) + `remark-gfm` (MIT) for tables/strikethrough/task lists |
| sanitise | `rehype-sanitize` (MIT) with a restrictive schema; **no `rehype-raw`**, so raw HTML in a document is shown as text, never executed |
| remote resources | override `urlTransform` to drop `http(s)://` images/links (or rewrite links to open in a new tab with `rel="noreferrer noopener"`); a CSP with `img-src 'self' blob: data:` enforces it even if a component is misused |
| source view | the `.md` text in a `<pre>` with a monospace font; toggle with the rendered view (tabs, not a switch, so the state is obvious) |
| long documents | the real corpus produces 1.7–1.9 MB markdown for big workbooks; render with a cap (e.g. first 5 000 blocks) and a "download the rest" hint, or virtualise the block list |
| copy / download | `navigator.clipboard.writeText` with a textarea fallback; download via `Blob` + `URL.createObjectURL` (no server round trip needed — the bytes were already fetched) |
| code blocks | `remark-gfm` + a highlighter is optional for v1; the corpus is prose and tables, not code |
| non-UTF-8 | results are UTF-8 by contract (`charset=utf-8`); only the *original* preview needs decoding |

No existing component in this repo: `sdk/python/parsedock_sdk` is Python and there is no web
code, so the choice is unconstrained (verified: no `package.json`).

## 5. Frontend ↔ backend integration

| Question | Answer |
| --- | --- |
| Is there a frontend? | No. |
| CORS? | **Absent** — no `Access-Control-*` anywhere (`grep -rni cors internal cmd` is empty). A browser page on another origin cannot call the API today. |
| Dev proxy / BFF? | None. The api mux serves only `/v1`, `/healthz`, `/readyz`; there is no static file serving. |
| API key in the browser? | **Must not happen.** Keys grant `jobs:write/read/ack` for an owner; a bundled key is readable in devtools and would be a shared credential for everyone who opens the page. |
| Can the browser call the API directly? | Only with CORS **and** the key in the page — both undesirable. |
| SSE through a proxy? | Yes, as long as the proxy does not buffer. The backend already sets `X-Accel-Buffering: no` and flushes per event; nginx needs `proxy_buffering off;` (and `proxy_read_timeout` above the stream duration). The api gateway itself preserves `http.Flusher` (`headerWriter.Flush`, PR-02), so SSE works through it. |
| `EventSource` and auth | **`EventSource` cannot send an `Authorization` header.** With a same-origin proxy that injects the key, `new EventSource('/v1/jobs/'+id+'/events')` works unchanged. Cross-origin would require a fetch-based SSE parser. This is a second, independent reason to use a proxy. |
| Large uploads | The gateway caps the body and streams to the coordinator (no buffering); `fetch` + `FormData` + `File` streams the blob without copying it into JS memory. Show upload progress with `XMLHttpRequest.upload.onprogress` (fetch has no upload progress) or accept an indeterminate bar. |
| TypeScript client | `make sdk` regenerates **python** and **java** only (`Makefile`); there is no TS client. Add `openapi-typescript` (MIT, dev-only) to type a hand-written ~150-line client, or add `-g typescript-fetch` to `make sdk`. The hand-written client is preferable for v1: the generated one will not model SSE. |
| Compose integration | Add one service to a new `deploy/compose.playground.yaml` (profile `playground`) that builds the SPA and serves it with nginx, proxying `/v1/` to `api-1` with the key injected from an env var. No change to existing services. |

**Recommended topology (MVP):**

```
browser ──HTTP──▶ playground (nginx: static SPA + /v1 proxy, injects Authorization)
                        └──▶ api-1 (8080) ──▶ coordinator ──▶ worker
```

Two consequences to design for: every visitor shares the proxy's key/owner (fine for an internal
developer tool; a public playground needs per-session keys, §12), and the proxy must not expose
`/internal/*` (it only proxies `/v1/`).

## 6. File lifecycle

| Step | UI behaviour | Backend behaviour it relies on | Verified |
| --- | --- | --- | --- |
| upload | keep the `File` for preview; `mode` from the selector; no `ttl_seconds` unless the user asks | `202` + `result_expires_at` later; default TTL 1 h, ceiling 24 h | `service.go` `CreateJob`, `Complete` |
| progress | SSE `snapshot`/events; `progress_percent`/`progress_stage`; fall back to polling every 1 s if SSE fails | stream ends with `stream_end`; status polling shares the per-owner rate budget (20 rps default) | `handleEvents`, PR-09 |
| result | fetch markdown (and IR for warnings), then display; **never ACK on 200** | download creates a 60 s lease that is renewed server-side | `BeginDownload`, `leaseRenewer` |
| release | explicit button → `POST /ack` with `result_version` from the response (`X-Result-Version` or the job view) | requires completed + exact version; deletion is async (`release_pending`) | `service.go` `Ack` |
| expiry | show `result_expires_at` countdown; on `410` show "released/expired, re-upload to parse again"; on `404` "gone" | tombstone window 10 min, then `404` | `docs/JOB-LIFECYCLE.md`, `handleResult` |
| leaving the page | nothing to do: without an ACK the server expires the result | TTL + cleaner | `cleanup.go` |

Two caveats for the UI: (a) `cancel` on an already completed job returns `409` and needs
`result_version` to release instead — the button must adapt; (b) do **not** use `/v1/parse` for
the main flow because it ACKs automatically and returns only the IR.

## 7. Backend changes required

Nothing is strictly required for a same-origin MVP. Recommended, in order of value:

| Change | Why | Size | Blocking? |
| --- | --- | --- | --- |
| `deploy/compose.playground.yaml` + nginx config (key injection, `/v1/` proxy, `proxy_buffering off`, CSP) | key never reaches the browser; SSE works; no CORS needed | S (config only) | no |
| `--cors-allow-origin` on the api role | lets the SPA run without a proxy (e.g. `vite` pointing at a remote stack) — still requires the user to paste a key | S (~30 lines + test) | no |
| `GET /v1/capabilities` | the UI hardcodes the accepted extensions today; a deployment with fewer engines would lie. Returns formats, modes, engines, max upload, TTL bounds | S | no |
| `GET /v1/jobs/{id}/input` (scope `jobs:read`, honours a lease) | restore the left pane after a page reload / share a link | M (storage + lease + tests) | no |
| `POST /v1/parse` `format` + `no_ack` | only if the UI ever wants a synchronous path | S | no |
| Compose default `--max-upload-mb=64` → 128 for the playground | real audit files are 52–74 MiB, so the default rejects them | trivial (compose flag) | no |

## 8. Security and performance risks

**Security**

1. **Key leakage** — the whole design forbids a key in the bundle; the proxy injects it. Never
   name it `VITE_*` (Vite inlines those), never put it in a query string (logged), never commit
   `deploy/.env` (already git-ignored).
2. **Untrusted markdown** — documents are attacker-controlled. No `rehype-raw`, `rehype-sanitize`
   on, `urlTransform` drops remote URLs, and a CSP (`default-src 'self'; img-src 'self' blob:
   data:; frame-src blob:; connect-src 'self'`) as defence in depth.
3. **HTML/DOCX previews** — `<iframe sandbox>` without `allow-same-origin` (no script execution,
   no storage access); DOCX preview injects HTML into the page, so render it inside a sandboxed
   iframe too, not into the app DOM.
4. **`/internal/*` exposure** — the proxy must only forward `/v1/`. Metrics and worker endpoints
   never go public.
5. **Quota/rate abuse** — a shared key means a shared owner budget (concurrent jobs, 24 h bytes,
   storage). A public deployment needs per-session keys and a smaller quota.
6. **Privacy** — the UI must state that the file leaves the browser and is stored server-side
   until TTL/ACK, and that previews are local. `docs/history/AUDITS.md` records that document content is
   never logged; the UI must not log it either.

**Performance**

1. **Polling budget** — 20 rps/burst 40 per owner shared by *all* requests; a 500 ms status poll
   plus retries hits `429`. Prefer SSE (one connection) and exponential backoff with
   `Retry-After`.
2. **Large results** — markdown of 1.7–1.9 MB and IR of 13–19 MB exist in the real corpus; do not
   `JSON.parse` a 19 MB IR just for warnings, and cap rendered blocks.
3. **Memory** — revoke object URLs (`URL.revokeObjectURL`) on unmount and on new upload; avoid
   holding both the `File` and a full `ArrayBuffer` copy for a 100 MB file.
4. **First paint** — code-split the preview libraries (pdf.js, docx-preview, exceljs) so the
   landing page stays small.

## 9. Docker integration

```yaml
# deploy/compose.playground.yaml (sketch; profile keeps the CPU stack unchanged)
services:
  playground:
    build: { context: .., dockerfile: docker/Dockerfile.playground }   # node build → nginx
    ports: ["${PLAYGROUND_PORT:-8090}:8080"]
    environment:
      PARSEDOCK_API_UPSTREAM: http://api-1:8080
      PARSEDOCK_PROXY_TOKEN: ${PARSEDOCK_API_KEYS%:*:*}   # or a dedicated key
    networks: [frontend]
    depends_on: [api-1]
```

nginx essentials: `proxy_pass http://api-1:8080;` for `/v1/`, `proxy_set_header Authorization
"Bearer $PARSEDOCK_PROXY_TOKEN";`, `proxy_buffering off;` (SSE), `proxy_read_timeout 600s;`,
`client_max_body_size 140m;`, `location /internal/ { return 404; }`, and a CSP header. The SPA
build must be reproducible (lockfile committed) and must contain no secrets — verify with a
grep for the key in `dist/` in CI.

## 10. Recommended stack

| Layer | Choice | Why |
| --- | --- | --- |
| build | Vite 7 + TypeScript 5 | fast, zero-config static output, dev proxy for the API key |
| UI | React 19 + Tailwind CSS + Radix primitives (MIT) | mature, accessible primitives; Tailwind keeps the "developer tool" look bespoke instead of an admin template |
| state | TanStack Query (MIT) for the job/result fetches + one custom SSE hook | retries/backoff for free, SSE does not fit a query cache |
| split panes | `react-resizable-panels` (MIT) | tiny, keyboard-accessible, persists sizes |
| markdown | `react-markdown` + `remark-gfm` + `rehype-sanitize` | no raw HTML path, tables included |
| PDF | native `<iframe>` for v1, `pdfjs-dist` (Apache-2.0) later | exact rendering with zero bundle cost in v1 |
| DOCX / XLSX | `docx-preview` (MIT) / `exceljs` (MIT) | both permissive, no CDN dependency (note: SheetJS CE is also Apache-2.0 if preferred) |
| CSV | `papaparse` (MIT) | delimiter/encoding handling |
| types | `openapi-typescript` (MIT, dev-only) from `api/openapi.yaml` | keeps the client honest without a runtime dependency |
| tests | Vitest + Testing Library, Playwright (Apache-2.0) for E2E | Playwright can drive the real Compose stack |

Licence check matters here: the repo ships `scripts/license_audit.sh` and refuses GPL in
distributed images, so the list above is deliberately MIT/Apache-2.0 only.

## 11. Minimum MVP

One page, three states (idle → parsing → result):

1. drop zone + file picker; client-side type/size validation against the accepted list; explicit
   "the file is uploaded to the server" notice.
2. mode selector (auto/fast/accurate); advanced row (device, TTL) collapsed.
3. progress: stage text + percentage from SSE; cancel button while queued/running.
4. result: left = original preview (PDF/images/text/CSV), right = markdown with
   Rendered/Source tabs, copy, download; metadata strip (engine, pages, chars, tables, warnings,
   parse ms, `result_version`, expires-at countdown).
5. explicit **Release server copy** (ACK) + correct `410`/`404`/`409` handling; no auto-ACK.
6. error panel mapping `error_code` → a human sentence (the enum is in `api/openapi.yaml`).
7. DOCX/XLSX/PPTX/legacy: metadata + "preview not available in this version" (parse output is
   still shown) — this keeps v1 small without lying to the user.

Out of scope for v1: accounts, history, saved jobs, batch upload, diffing, sharing links, GPU
selector (unless a GPU worker is registered), i18n.

## 12. Phased plan and task slices

| Phase | Scope | Done when |
| --- | --- | --- |
| P0 | `web/` scaffold (Vite+TS+Tailwind+Vitest), lint/build in CI, typed client from the OpenAPI doc | `npm ci && npm run build && npm test` green in a new CI job; no bundle secrets (grep in CI) |
| P1 | upload + polling + result (markdown/IR) + PDF/image/text preview + split panes | one real PDF end to end against the Compose stack |
| P2 | SSE progress + cancel + explicit ACK + 410/409 handling + metadata/warnings | Playwright: upload → progress → result → release → 410 |
| P3 | Office previews (DOCX, XLSX, CSV table), HTML sandbox, download/copy polish, dark mode | preview matrix §3 for those formats |
| P4 | nginx + `compose.playground.yaml`, CSP, docs (`docs/PLAYGROUND.md`), accessibility pass | `docker compose --profile playground up` serves the app with no key in `dist/` |

Independently mergeable slices (each is a small PR):

1. `web/` scaffold + CI job + `openapi-typescript` script.
2. typed API client + unit tests against a mocked `fetch` (`jobs.create/get/result/ack/cancel`).
3. drop zone + validation + upload progress.
4. split-pane shell + preview registry (pdf/img/text/csv) + object-URL lifecycle tests.
5. markdown viewer (render/source/copy/download) + sanitizer test with a hostile document.
6. SSE hook + polling fallback + progress/cancel UI.
7. result metadata strip + warning fetch + error catalogue.
8. ACK/release + 410/expired UX.
9. nginx image + compose profile + CI check that `dist/` contains no key.
10. Playwright E2E against Compose (one fixture PDF, one DOCX, one CSV).

## 13. Open decisions

1. **Auth model**: proxy-injected shared key (internal tool, simplest) vs per-visitor keys and a
   small BFF (public playground) vs "paste your key" (no proxy, needs CORS). Recommendation:
   proxy for v1; revisit before any public exposure.
2. **Public exposure**: rate limits (20 rps) and quotas (concurrent jobs) are per owner, so a
   public page would throttle everyone together. If the playground is ever public, per-session
   keys are mandatory.
3. **Input re-download** (`GET /v1/jobs/{id}/input`): needed for reload/deep links; not needed for
   the first flow. Decide before designing shareable URLs.
4. **Deep links**: encode `?job=<id>` in the URL (works only while the result exists) — cheap, but
   depends on decision 3 for the left pane.
5. **Playground engine set**: `xberg` only (fast, small image) vs `+docling-rs` (accurate, ~1 GB
   of models). Affects the compose profile and the "Accurate" mode being meaningful.
6. **OCR language**: the corpus is Chinese; the deployment default is `eng`
   (`docs/DEPLOYMENT.md`). A playground over Chinese material should run `--xberg-ocr-lang zh`
   (or a second worker with `eng`), otherwise the scans look worse than they are.
7. **Max upload** for the playground: 64 MiB (compose default) vs 128 MiB (the corpus runs) —
   52–74 MiB audit files need the latter.
8. **Preview fidelity disclaimer**: how prominent should "Office previews are approximate" be?
9. **Bilingual UI**: Chinese-only, English-only, or both (the corpus is Chinese, the docs are
   English).

## Appendix — verification commands used

```bash
git log -1; git status --porcelain; gh pr list --state all
ls; find . -maxdepth 2 -name package.json
sed -n '/func (s \*Server) PublicMux/,/^}/p' internal/coordinator/http.go
sed -n '/func (s \*Server) mux/,/^}/p' internal/api/server.go
grep -rniE "access-control|cors" internal cmd            # → nothing
sed -n '/func NeededScope/,/^}/p' internal/api/keys.go
go test ./... -count=1; gh run list --branch main --limit 2
node -v; npm -v
```
