# PR-A report — opt-in xberg OCR backend

> Shipped: PR #32 (`32ae998`), branch `feat/xberg-ocr-backend`, CI green (10 jobs).
> Scope: one new flag, startup validation, routing-reason suffix, tests,
> documentation. **No default behaviour change.**

## 1. What was built

| Item | Detail |
| --- | --- |
| Flag | `--xberg-ocr-backend` / `PARSEDOCK_XBERG_OCR_BACKEND` |
| Default | `""` → **no `--ocr-backend` argument is emitted at all**, so xberg keeps its own default (tesseract) |
| Values | the eight backends xberg 1.3.10 registers; anything else fails **at startup** |
| Validation | `engines.ValidateOCRBackend`, called once in `run()`; the list is pinned in `engines.OCRBackends` and a unit test fails if it drifts from the binary's own error message |
| Auditability | `Document.Routing.Reason` gains `(ocr_backend=<name>)` **only** when a backend is explicitly configured |
| Deployment | `deploy/host/{docker-compose.yml,.env.example}` pass the variable through (empty is safe) |
| Docs | `docs/DEPLOYMENT.md` config table, `docs/OCR-ROUTING-PROPOSAL.md` P1 marked implemented, `docs/OCR-BACKEND-RESEARCH.md` §3.5 language correction |

## 2. The five required verifications

### 2.1 Does the binary really support the backend and the options?

**Backend: yes.** Unknown names are rejected by the binary itself:

```
$ xberg extract --ocr true --ocr-backend bogus f.pdf
Error: Invalid OCR backend 'bogus'. Valid backends: tesseract, paddle-ocr,
sceptre, vlm, candle-trocr, candle-paddleocr-vl, candle-glm-ocr, candle-deepseek-ocr
```

ParseDock mirrors that list and fails at startup instead of per job:

```
OCRB-05  unknown backend rejected at startup (exit=1)
```

**Options: no — deliberately not exposed.** `--ocr-backend-options '{"tier":"server"}'`,
`{"model":"v6:medium"}` and `{"nonsense":1}` all **succeed silently** and the
engine keeps `model_key=v6:small tier="mobile"`:

```
INFO xberg::paddle_ocr::backend: Initializing PaddleOCR engine family="chinese"
     model_key=v6:small tier="mobile" backend=Ort
```

A flag whose effect cannot be observed is worse than no flag, so PR-A ships the
backend selector only.

### 2.2 Do the language codes take effect?

Yes, and **no mapping is needed** — xberg normalises both spellings (read from the
same init log line):

| `--ocr-language` | family selected |
| --- | --- |
| `eng`, `en` | `english` |
| `chi_sim`, `zh`, `ch` | `chinese` |
| `fra` | `latin` |

`eng` is therefore a valid English code for paddle-ocr, not a silent fallback to
Chinese. This corrects an assumption in the earlier spike document (§3.5 there).

### 2.3 Does the CPU worker run with existing models, and no new dependency?

Yes. The PP-OCRv6 graphs are inside the pinned xberg tarball; nothing is
downloaded, and the worker's dependency set is unchanged:

```
OCRB-06  no model files written by the worker (0 found)
```

Peak RSS and latency on the 2-page Chinese scan (same host, same method as
`docs/OCR-GPU-BENCHMARK.md`): **1 252 ms, 765 MiB** for paddle-ocr vs
**406 ms, 224 MiB** for tesseract `chi_sim`.

### 2.4 End-to-end behaviour in `fast` and `auto`

Real standalone process, real engines (`tests/integration/ocr_backend.py`):

| Case | Engine | CER | `ir_reason` | Warnings |
| --- | --- | --- | --- | --- |
| default, `fast`, English scan | xberg | 0.000 | `xberg native/ocr pipeline` | `ocr_low_confidence` |
| `paddle-ocr`, `fast`, Chinese scan | xberg | **0.000** | `xberg native/ocr pipeline (ocr_backend=paddle-ocr)` | `ocr_low_confidence` |
| `paddle-ocr`, `auto`, Chinese scan | **docling-rs** | 0.002 | `format=pdf mode=auto …` | – |
| default, `auto`, Chinese scan | xberg | 0.453 | `format=pdf mode=auto …` | `ocr_low_confidence`, `degraded_conversion` |

The `auto` rows are **evidence for the PR-B revision**, not PR-A defects: with
`paddle-ocr` a CER-0.000 result is escalated anyway, and with the default
`eng` tesseract a 45 %-CER result is **not** escalated. Both are analysed in
`docs/OCR-PR-B-DESIGN.md`.

### 2.5 Is the default really unchanged?

Verified three ways:

1. **Argv**: `TestXbergArgsDefaultHasNoOCRBackend` asserts the default argv has no
   `--ocr-backend` token (and `--ocr true`, `--ocr-language eng` are still there).
2. **End to end**: OCRB-01 produces `ir_reason='xberg native/ocr pipeline'` with
   no suffix and CER 0.000 on the English scan — the same string the pre-PR code
   produced.
3. **Regression suite**: `go test ./...`, `pytest tests/unit` (62), `ci-local
   static`, and the 10 CI jobs pass.

`--ocr false` plus a configured backend does not leak the flag either
(`TestXbergArgsNoBackendWhenOCRDisabled`).

## 3. Rollback

Unset the variable. Nothing else reads it; no schema, API, or storage change was
made.

## 4. What this report could not verify

* Real-corpus OCR quality: the fixtures are synthetic (one font per language).
* Whether `paddle-ocr` is faster *warm*: all numbers include a cold model load
  (≈0.2 s for paddle-ocr, ≈1.4 s for docling-rs).
* Whether `paddle-ocr` handles rotated, stamped, or low-dpi scans — untested.
