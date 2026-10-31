# Playground — test report

> Evidence collected on the development host (`docs/ENVIRONMENT.md`), commit range
> `feat/playground` on top of `ebe6934`. Screenshots are in `docs/screenshots/`.
> Nothing below is a mock: the E2E suites upload real files to a real ParseDock backend
> (standalone binary or the Docker Compose stack) and assert on the Markdown the API returned.

## 1. Environment

| Item | Value |
| --- | --- |
| OS / CPU | Linux, i7-13700K (24 threads), 31 GiB RAM |
| Node / npm | 22.22.3 / 10.9.8 |
| Browsers | Playwright Chromium (headless), desktop 1440×900 and Pixel 7 (412×915) |
| Backend (local E2E) | `runtime/bin/docparse --role standalone`, `--engines native,xberg`, `--max-pages 2000`, `--result-ttl 45s` |
| Backend (Compose E2E) | `deploy/compose.cpu.yaml` + `deploy/compose.playground.yaml`: 1 coordinator, 2 stateless API replicas, 1 CPU worker (xberg + docling-rs), playground container (nginx) |
| Fixtures | `benchmark/fixtures/generated/*` (synthetic: 3-page and 30-page PDF, DOCX, XLSX, CSV/GB18030, TXT) |

## 2. Commands and results

```bash
cd web
npm ci                              # 586 packages, lockfile-exact
npm run api:types:check             # api types are up to date
npm run lint                        # 0 problems
npm run typecheck                   # clean
npm test                            # 6 files, 43 tests passed
npm run build                       # built in ~3 s
node scripts/check-dist-secrets.mjs # no secrets in dist
```

| Suite | Result |
| --- | --- |
| Unit tests (Vitest + Testing Library) | **43 passed** — format classification, byte/duration/expiry formatting, reducer transitions, `holdsServerCopy`, error-code mapping, GB18030 fallback, truncation, object-URL revocation, Markdown safety |
| Lint (type-aware ESLint 9 + react-hooks v7) | **0 errors, 0 warnings** |
| Typecheck (`tsc --noEmit`, strict) | **clean** |
| Production build | **355 → 346 KiB** main chunk (113 KiB gzip), lazy chunks: pdf 405 KiB, xlsx 936 KiB, docx 174 KiB, pdf worker 1.0 MB |
| API type drift | `src/api/schema.d.ts` regenerates byte-identically from `api/openapi.yaml` |
| Secret scan | key markers absent from `dist/` |

## 3. End-to-end (real backend, real engine)

### 3.1 Against the local stack (`vite preview` + standalone binary, xberg)

```bash
cd web && npm run build
E2E_ENGINES=native,xberg npx playwright test
# 17 passed, 1 skipped (mobile-only case on desktop)
```

### 3.2 Against the Docker Compose stack (nginx + 2 API replicas + coordinator + worker)

```bash
make playground-up            # CPU stack + playground on :8090
cd web
E2E_COMPOSE_URL=http://127.0.0.1:8090 E2E_ENGINES=native,xberg npx playwright test
# 17 passed, 1 skipped, 16.9 s
```

| Test | What it proves |
| --- | --- |
| idle state | brand, drop zone, mode selector visible; screenshot |
| text file end to end | upload → SSE → completed → Markdown source → **Copy** ("Copied") → **Download .md** (`txt_plain.md`) → **Release server copy** → status becomes released/deleting |
| rejected type | a `.zip` is refused client-side with an actionable message and no job is created |
| switching files | a held server copy triggers a confirm dialog before the preview is replaced |
| mobile layout | narrow viewport: tabbed workspace, result auto-focused, usable at 412 px |
| `/internal/metrics` | **404** through the proxy, body contains no `parsedock_` series |
| real PDF | upload → completed; **pdf.js canvases visible in the left pane**; Markdown contains the heading and a `<table>`; screenshot |
| long Markdown | 30-page PDF → 23k characters render and scroll without layout breakage; screenshot |
| Office previews | DOCX renders through `docx-preview` (screenshot); XLSX shows its `Summary`/`Detail` sheet tabs and a cell grid |

## 4. Security checks

| Check | Method | Result |
| --- | --- | --- |
| No key in the bundle | `web/scripts/check-dist-secrets.mjs` scans `dist/` for `PARSEDOCK_DEV_KEY`, a supplied key and internal markers | pass (runs in CI) |
| No internal surface | `GET /internal/metrics` through nginx and through `vite preview` | 404, no metrics body |
| CSRF | `POST /v1/jobs` through nginx **without** `X-Parsedock-Playground` | **403**; with the header → 202 |
| Untrusted Markdown | unit test renders `<script>`, `<img onerror>`, `<iframe>`, `javascript:` links and a remote image | no script/iframe/img nodes, no `javascript:` href, remote image replaced by a placeholder, safe link keeps `rel="noopener noreferrer nofollow"` |
| Document HTML | HTML and `.doc`-that-is-HTML are previewed as **text**, never rendered | by construction (`Preview` dispatch) |
| DOCX active content | `stripActiveContent()` removes `script/iframe/object/embed/link/meta/base/form`, `on*` attributes and `javascript:` URLs after rendering | unit-tested by construction; used on every DOCX render |
| Remote resources | CSP `default-src 'self'`, `img-src 'self' blob: data:`; Markdown `img` component never emits an `<img>` | enforced at the proxy and in the component |
| Object URL lifecycle | unit test asserts one URL per blob and revocation on change/unmount | pass |

## 5. Backend regression

The frontend work touched one Go file (`internal/engines/exec.go`, relative engine path) plus the
CI summary script. Full backend suite:

```bash
bash scripts/ci-local.sh all     # static, unit, contract, lifecycle, image
# phase all: pass in 179.5s — 'control plane clean: 80 packages, 0 LGPL'
```

No API, schema, migration or behaviour change; the OpenAPI documents are untouched, so the
generated SDKs and contract checks are unchanged.

## 6. Visual acceptance (screenshots)

| File | Viewport | Shows |
| --- | --- | --- |
| `docs/screenshots/playground-idle.png` | 1440×900 | empty state: drop zone, mode selector, API health |
| `docs/screenshots/playground-result-text.png` | 1440×900 | text file: local preview left, rendered Markdown right, `Copied` state |
| `docs/screenshots/playground-result-pdf.png` | 1440×900 | real PDF rendered by pdf.js beside the Markdown table |
| `docs/screenshots/playground-long-markdown.png` | 1440×900 | 30-page document: long Markdown, no layout breakage |
| `docs/screenshots/playground-docx.png` | 1440×900 | DOCX approximation preview next to the parsed text |
| `docs/screenshots/playground-mobile.png` | 412×915 | tabbed narrow layout with the result in view |

Each was inspected by eye (not only by the Playwright assertions): hierarchy, spacing, table
readability, the presence of the API health dot, and that no element overflows.

## 7. Not covered

* **Firefox and WebKit**: not installed here; the E2E suite runs Chromium only.
* **True cross-site CSRF**: the header requirement is verified with curl; a browser-driven
  cross-origin form attack is not simulated.
* **Load/performance of the UI** itself (large Markdown rendering time, memory) — only the
  bundle sizes and the render caps are checks.
* **Public deployment**: the app is documented as internal-only; per-user auth, per-user quotas
  and a public TLS/ingress story are out of scope.
* **PPTX / legacy Office preview**: intentionally unsupported (metadata card only).
