# ParseDock Playground (web)

A single-page workbench: upload a document, watch it parse, and compare the original with the
Markdown the API returns. It is an internal developer tool — not a document manager.

```bash
npm ci
# A backend must be running; the dev server proxies /v1 and injects the key server-side.
PARSEDOCK_API_UPSTREAM=http://127.0.0.1:8080 PARSEDOCK_DEV_KEY=dev-key npm run dev
```

`PARSEDOCK_DEV_KEY` is read by the **Node** process (Vite proxy), never bundled: only `VITE_*`
variables are exposed to the browser, and this one deliberately is not.

| Script | What it does |
| --- | --- |
| `npm run dev` | dev server on :5173 with the authenticated `/v1` proxy |
| `npm run build` | typecheck + production build into `dist/` |
| `npm run preview` | serve `dist/` on :4173 with the same proxy (used by E2E) |
| `npm test` | Vitest unit tests |
| `npm run lint` / `npm run typecheck` | ESLint (type-aware) / `tsc --noEmit` |
| `npm run api:types` | regenerate `src/api/schema.d.ts` from `api/openapi.yaml` |
| `npm run api:types:check` | fail when that file is stale (CI) |
| `npm run e2e` | Playwright; starts a real backend and the preview server |

## E2E against a real backend

`playwright.config.ts` starts `runtime/bin/docparse` (mode `standalone`) and `vite preview`.
What the backend can parse is chosen by `E2E_ENGINES`:

```bash
make build                                  # at repo root
cd web && npm run build
E2E_ENGINES=native,xberg npx playwright test   # real PDF, DOCX, XLSX (needs runtime/engines)
E2E_ENGINES=mock,native npx playwright test    # fast, text formats only
# against the Docker Compose stack instead:
E2E_COMPOSE_URL=http://127.0.0.1:8090 npx playwright test
```

Screenshots for visual review are written to `../docs/screenshots/`.

## Layout

```
src/
  api/          generated schema + a thin fetch client (no key handling)
  app/          App shell, providers
  components/   TopBar, Dropzone, Intake, Panes, StatusStrip, MetaStrip, MarkdownPane, dialogs
  features/     playground state machine, SSE hook, formatting helpers
  preview/      PDF (pdf.js), images, text, CSV, DOCX, XLSX, unsupported
  hooks/        object URL lifecycle, media query
  __tests__/    unit and safety tests
e2e/            Playwright specs + fixture paths
scripts/        API type drift check, dist secret scan
```

## Rules this app keeps

* The API key never reaches the browser; a same-origin proxy injects it.
* Nothing is ACKed automatically — releasing the server copy is an explicit button, and
  closing the tab falls back to the server's TTL.
* Document HTML is never executed and remote images are never fetched (sanitised Markdown,
  sandbox-free previews built from parsed data, CSP at the proxy).
* Previews use the local `File`; object URLs are revoked when the file changes or the pane
  unmounts.
