# ParseDock Playground — implementation notes

> Built on `main` after PR #27, following `docs/briefs/ParseDock-Playground-Implementation-Guide.md`
> (PG-01…PG-10). Baseline check in `docs/history/PLAYGROUND-READINESS.md`; test evidence in
> `docs/history/PLAYGROUND-TEST-REPORT.md`.

## 1. What it does

One page, three states: **choose → parse → compare**.

* **drop a file anywhere on the page** (a page-wide overlay appears, not just the drop zone) or
  browse for one (PDF, DOCX, XLSX/XLSM, PPTX, CSV/TSV, TXT/MD, PNG/JPEG/WebP, HTML,
  legacy `.doc/.xls/.ppt`), pick `auto | fast | accurate`;
* watch the real job: queued → running (SSE, with polling as a fallback) → completed/failed;
* left pane: the **original file, previewed locally** from the `File` the browser already holds;
  right pane: the **Markdown the API returned**, with `Preview` / `Markdown source`, copy and
  `Download .md`;
* cancel a queued/running job; when a result exists, **release the server copy** explicitly;
  without that, the server's TTL cleans it up (`result_expires_at` is shown as a countdown).

The page never ACKs on its own: not on HTTP 200, not on download, not on unmount, not on
`pagehide`.

The UI is **bilingual (English / 简体中文)**: the header has an explicit switch, the default
follows `navigator.language`, the choice is remembered in `localStorage`, and `<html lang>` is
updated. Every string — including error-code sentences, the confirm/help dialogs and the preview
limit messages — goes through `t()`, so a missing translation is a type error.

## 2. Architecture

```
browser ──GET /              ──▶ nginx: static SPA (dist/)
        ──/v1/* (fetch+SSE)  ──▶ nginx: adds Authorization: Bearer <key>, streams ──▶ api-1
                                   (never proxies /internal/*, rejects CSRF-less writes)
```

* **No backend change was needed for the UI.** The public API already has everything: async
  create, status, SSE events, result in three formats, ACK, cancel, probes.
* **The key never reaches the browser.** In development and in `vite preview` the key comes from
  `PARSEDOCK_DEV_KEY` in the **Node** process (Vite `server.proxy`/`preview.proxy` inject the
  header); in production nginx does the same from `PARSEDOCK_PROXY_TOKEN`. Only `VITE_*`
  variables are inlined by Vite, and the key is deliberately not one of them.
* **Same-origin is also a technical requirement**: `EventSource` cannot send an
  `Authorization` header, so SSE only works through a proxy that adds it.
* **CSRF.** Because the proxy adds the credential, a cross-site form could otherwise act as the
  owner. Every state-changing request must carry `X-Parsedock-Playground: 1` (set by
  `src/api/client.ts`), which nginx enforces with a small state machine (nginx forbids nested
  `if`); a plain HTML form cannot set a custom header.
* **Vite dev/preview** proxy the same paths, so the local E2E suite exercises the production
  request shape.

### Frontend layers

| Layer | Files | Notes |
| --- | --- | --- |
| API | `src/api/schema.d.ts` (generated from `api/openapi.yaml`), `client.ts`, `errors.ts` | ~40 lines per endpoint; `ApiError` maps the stable error codes to sentences |
| State | `features/playground/phase.ts` + `usePlayground.ts` | one reducer; the backend's `status`/`retention_status` are the only truth, `phase` adds `uploading`/`releasing` |
| Events | `features/playground/useJobEvents.ts` | `EventSource` on `/v1/jobs/{id}/events?scope=processing`; closes on error and hands over to polling with backoff |
| Preview | `src/preview/*` | PDF (pdf.js), image, text (encoding-aware), CSV (PapaParse), DOCX (`docx-preview`), XLSX (`exceljs`), unsupported card |
| UI | `src/components/*` | own design tokens (Tailwind `@theme`), Radix `Slot` for buttons, resizable panes, native `<dialog>` for help/confirm |
| i18n | `src/i18n/` | typed dictionaries (`en`, `zh-CN`), `I18nProvider` + `t()`, browser-language default, explicit toggle in the header, choice persisted in `localStorage`, `<html lang>` updated |

## 3. Decisions and why

| Decision | Why |
| --- | --- |
| **Same-origin proxy instead of CORS** | no backend change, no key in the page, SSE works. CORS would still leave the key in the browser. |
| **`/v1/parse` is not used** | it returns the IR only (no `format` choice, no SSE) and leaves the result to the TTL, so the Playground would still need an explicit release path — the job API gives both. |
| **pdf.js instead of the browser's PDF plugin** | the E2E runner (headless Chromium) has no plugin, and the built-in viewer gives no control over scale. Pages render lazily, capped at 60. |
| **`react-markdown` + `remark-gfm` + `rehype-sanitize`, no `rehype-raw`** | untrusted documents; raw HTML is escaped, not executed. Remote images are replaced by a placeholder so reading a document never calls a third party. |
| **TanStack Query only for the health probe** | job state needs ordering guarantees (SSE + a late response must not overwrite a newer job); that is a reducer, not a query cache. `QueryClient` is still the provider for the app. |
| **Office renderers are lazy chunks** | `docx-preview` (~174 KB) and `exceljs` (~936 KB) load only when a DOCX/XLSX is previewed; the initial bundle is 355 KB (113 KB gzip). |
| **Preview caps** | PDF 60 pages, DOCX 20 MiB, XLSX 16 MiB / 5 sheets / 200 rows / 30 cols, CSV 200 rows, text 512 KiB, IR metadata 4 MiB. Beyond that the pane says so and the parsed Markdown is still shown. |
| **Object URLs are derived, not stored** | `useObjectUrl` memoises the URL and revokes it in the effect cleanup, so switching files cannot leak a blob. |

## 4. API usage (verified against `api/openapi.yaml` and the handlers)

| Step | Call | Fields used |
| --- | --- | --- |
| upload | `POST /v1/jobs` (multipart `file`, `mode`) | `202` → `job_id`, `status`, `created_at`, `result_expires_at` |
| progress | `GET /v1/jobs/{id}/events?scope=processing` | `snapshot`/progress events (`progress_percent`, `progress_stage`) and terminal events; `stream_end` ends the stream |
| status (fallback) | `GET /v1/jobs/{id}` | `status`, `retention_status`, `progress_percent`, `progress_stage`, `engine`, `page_count`, `warning_count`, `error_code`, `result_version`, `result_expires_at` |
| result | `GET /v1/jobs/{id}/result?format=markdown` | body (UTF-8) + `X-Result-Version` (the ACK token; **not** the `ETag`) |
| metadata | `GET .../result?format=json` (capped at 4 MiB) | `metrics.parse_ms`, `tables`, `warnings[].code`, `routing.actual_device` |
| release | `POST /v1/jobs/{id}/ack` with `{"result_version"}` + `X-Result-Version` | `202`; a stale version is `409`, then status is polled until `released`/`expired` |
| cancel | `POST /v1/jobs/{id}/cancel` | `202`; on a completed job the API answers `409` (the UI only offers cancel while queued/running) |

## 5. Security model

1. **Credential**: injected by the proxy from the environment; `web/scripts/check-dist-secrets.mjs`
   fails CI if the key (or any `PARSEDOCK_*` marker) appears in `dist/`.
2. **CSRF**: custom-header requirement for non-GET (nginx). The dev/preview proxy does not enforce
   it, which is fine: it is a local tool with the key in the same process tree.
3. **Untrusted content**: Markdown is sanitised; document HTML is shown as text; DOCX previews are
   stripped of scripts/iframes/inline handlers (`stripActiveContent`) before being attached;
   remote images are never requested.
4. **CSP** (nginx): `default-src 'self'`, `img-src 'self' blob: data:`, `worker-src 'self' blob:`
   (pdf.js), `object-src 'none'`, no remote connect-src. `X-Content-Type-Options: nosniff`,
   `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
5. **Network boundary**: the playground container joins only the `frontend` network, nginx
   refuses `/internal/*`, and the Compose file publishes nothing else.
6. **Honest limitation**: the proxy hides the key but does not authenticate *people*. Anyone who
   can reach the port acts as one shared owner (concurrent jobs, daily bytes, storage). It is an
   internal tool; public exposure needs per-user auth and quotas first (guide §4).

## 6. Deviations from the guide

| Guide | Built | Why |
| --- | --- | --- |
| "PDF: `<iframe>`/`<object>`" | pdf.js canvas viewer | headless E2E and page-level control; the `<object>` fallback inside the component documents the plugin case |
| "TanStack Query for async requests" | Query for health only, reducer for the job | correctness of ordering beats cache conveniences here (§3) |
| "HTML preview as text" | text (source) | HTML is not rendered at all, which is the conservative reading |
| "Nightly runs the full E2E" | yes, plus a fast `mock,native` variant for PRs | xberg installation is minutes; the PR job still exercises the real HTTP/job/SSE/ACK path |

## 7. Backend changes

One, found by the E2E harness: `engines.RunCLI` resolves a **relative** `--xberg-bin` against the
operator's working directory instead of the attempt directory (`internal/engines/exec.go`).
Previously `--xberg-bin runtime/engines/xberg-cli/xberg` failed with "no such file" inside every
attempt (the same class of bug as the earlier relative `--work-dir`). Covered by
`TestRunCLIResolvesRelativeBinaryPath`. No API, schema or behaviour change.

## 8. Known limitations

* **PPTX and legacy `.doc/.xls/.ppt`** have no original preview (metadata card); the parsed
  Markdown is still shown. `docs/history/PLAYGROUND-READINESS.md` §3 explains why.
* **DOCX/XLSX previews are approximations**: layout is close, not pixel-identical; formulas are
  shown as text, no charts/pivots; large files are refused by the preview caps.
* **PDF preview stops at 60 pages** (the parse result covers all pages).
* **Only Chromium was exercised** by Playwright; Firefox/Safari were not installed in this
  environment. The code avoids Chromium-only APIs, but that is not tested.
* **The IR metadata fetch is capped at 4 MiB**: for very large workbooks the warnings and
  `parse_ms` chips show `–` rather than downloading 19 MB.
* **No deep links**: the job id is not put in the URL, because the left pane cannot be restored
  after a reload (the API has no input re-download). A refresh keeps the server job (TTL) but the
  page returns to idle.
* **Only two locales** (English, Simplified Chinese). The dictionaries are typed, so a new key
  must be translated in both; a third language means adding one file and one entry in `LOCALES`.
* **Rate limits**: the deployment's 20 rps/owner budget is shared with status polling; the app
  uses SSE, and backs off on `429`, but a public deployment needs its own limits.

## 9. Where things live

```
web/                       app, tests, E2E, Docker build context
docker/Dockerfile.playground   node build → nginx (pinned digests in images.lock.json)
deploy/nginx.playground.conf.template   proxy + CSP + CSRF + SSE settings
deploy/compose.playground.yaml          `--profile playground`
deploy/.env.example        PLAYGROUND_KEY / PLAYGROUND_PORT
docs/screenshots/          visual acceptance (1440 px and mobile)
scripts/ci-local.sh        unchanged; the web job lives in .github/workflows/ci.yml
```

Run it:

```bash
make playground-up        # CPU stack + playground on http://localhost:8090
make web-test             # lint, typecheck, unit tests, build, secret scan
make web-e2e              # Playwright against a local backend (needs make build)
```
