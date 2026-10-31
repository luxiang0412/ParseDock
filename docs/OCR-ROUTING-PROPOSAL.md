# OCR routing proposal — minimal changes, no rewrite

> Inputs: `docs/OCR-BACKEND-RESEARCH.md` (capabilities) and
> `docs/OCR-GPU-BENCHMARK.md` (numbers). Every proposal below is sized so it can
> ship on its own, with the current defaults unchanged until a benchmark approves
> a default change.

## 1. Where routing stands today

| Request | Engine | Notes |
| --- | --- | --- |
| text formats (`txt/md/csv/…`) | `native` | never OCR |
| `mode=fast` | `xberg` | OCR backend `tesseract`, language `--xberg-ocr-lang` (default `eng`) |
| `mode=accurate` | `docling-rs` | OCR engine `ppocr` (default), language mapped to `en`/`ch` |
| `mode=auto` | `xberg`, then one escalation | escalates to `accurate` when CJK ratio ≥ 0.15 **and** the `ocr_low_confidence` warning is present |
| `device=gpu_required` | `mineru` only | 409 `gpu_unavailable` when no GPU worker with free VRAM is registered |

## 2. Gaps, each with the evidence that makes it worth fixing

| ID | Gap | Evidence |
| --- | --- | --- |
| **G1** | OCR language is one global worker flag; a wrong value degrades text **silently** | Chinese scan read with `eng`: CER **0.453**; with `chi_sim`: 0.075; paddle-ocr `ch`: **0.000**. `quality score` stays 1.00 in all three cases |
| **G2** | The fast path uses tesseract for Chinese scans while a better engine is already inside the same binary | tesseract `chi_sim` CER 0.075 @ 406 ms vs xberg `paddle-ocr` CER **0.000** @ 1 252 ms (2-page scan, CPU) |
| **G3** | `--ocr-backend` cannot be selected from the deployment | the field exists in `internal/engines/xberg.go` (`OCRBackend`) but no CLI flag sets it |
| **G4** | `ocr_low_confidence` can only fire for tesseract | xberg returns `ocr_confidence.score=0.85, word_count=205` for tesseract but `score=null, word_count=0` for paddle-ocr |
| **G5** | Escalation is blind on pure scans | the trigger reads CJK ratio from *extracted text*; a Chinese scan read with `eng` yields Latin garbage, so the ratio is ≈ 0 and no escalation happens — the worst case never escalates |
| **G6** | docling-rs OCR knobs unused | `--ocr-engine`, `--ocr-mode`, `--ocr-scale` never passed; `--password` not exposed, so 8 encrypted corpus files fail as `encrypted_file` although both engines can decrypt |
| **G7** | GPU cannot be requested or refused from ParseDock's side | no EP flag reaches the engines; docling-rs **warns on stderr and falls back to CPU with exit 0**, so a "GPU" run can silently be a CPU run |
| **G8** | OCR element geometry is not exposed by any backend | no per-word/line bbox or confidence in xberg JSON; block bboxes are in **raster pixels** while the adapter labels table bboxes `pdf_user_space_v1` |

## 3. Proposal, ordered by (value ÷ risk)

### P1 — expose the OCR backend and its options (config only)

```
--xberg-ocr-backend        tesseract|paddle-ocr|…       (default: "" → tesseract, today's behaviour)
--xberg-ocr-backend-options '{"layout_mode":"whole_page"}'   (default: unset)
```

* No default change → byte-identical output unless an operator opts in.
* Deployment recipe: `PARSEDOCK_XBERG_OCR_BACKEND=paddle-ocr` for a CJK corpus.
* Language note that must go in the docs: with PP-OCRv6 `ch`, the same models read
  **both** Chinese and English at CER 0.000 on the dense fixtures, so `paddle-ocr`
  avoids the G1 trap; tesseract still needs the right `tessdata` language.
* Test: `TestXbergArgs` gains a case asserting the flags are passed only when set.

### P2 — make "quality unknown" visible so `auto` can escalate (small, uses existing vocabulary)

When the backend reports `ocr_confidence.score == null` **and** OCR ran on a page
with `scanned_confidence ≥ 0.7`, emit the warning ParseDock already defines —
`documentir.WarnOCRUsedUnknown` (`ocr_quality_unknown`).

* Fixes G4/G5 without inventing a confidence number (the spike rule: never
  fabricate confidence).
* `mode=auto` then performs exactly one escalation to `docling-rs`, which is
  measurably good on Chinese (CER 0.002) at ~1.7 s per 2 pages.
* Risk: more escalations → bounded by the existing "escalate once" rule.
* Test: unit test on the warning derivation + a lifecycle test asserting one
  escalation for a PP-OCR scan and none for a tesseract scan with confidence.

### P3 — docling-rs OCR engine/mode passthrough (no default change)

```
--docling-rs-ocr-engine  ppocr|tesseract   (default "" → upstream default: ppocr)
--docling-rs-ocr-mode    auto|full_page|layout_regions (default "")
```

* Lets an operator force `full_page` on documents whose layout model mis-crops
  (measured identical on our fixtures: 1 512 vs 1 540 ms, CER 0.000 — so this is
  an escape hatch, not a win).
* No default change → no output change.

### P4 — encrypted documents (product decision, additive API)

Both engines accept a password (`docling-rs --password`, and for xberg a page-level
retry would need the same). Proposal: optional `password` field on job creation,
stored only in memory for the attempt, never logged, never persisted with the
result; the error code stays `encrypted_file` when it is absent or wrong.

* Do **not** do this in the same PR as routing: it touches the public API, the
  worker protocol and the log-redaction rules.

### P5 — GPU: do not add an EP flag yet; add a *refusal* guard instead

The measurement (see benchmark doc) is that CUDA on this GPU is 18× slower and
changes output, while the engine hides a missing provider behind a warning. Two
consequences:

1. No `--docling-rs-ep` flag until there is hardware where CUDA wins.
2. Cheap safety net that is worth shipping even without GPU support: when a
   `device=gpu_required` attempt's engine stderr contains
   `built without that provider` / `using CPU`, fail the attempt with
   `gpu_unavailable` instead of returning a CPU result. That keeps the existing
   promise ("GPU required must fail loudly") true even if the engine lies.

### P6 — IR OCR elements: **not now** (blocked upstream)

No backend returns per-element bbox + confidence; emitting geometry from block
data would mean pretending precision we do not have. Preconditions before this is
revisited: (a) xberg exposes `ocr_elements` or tesseract word boxes in the JSON
contract, (b) a scanned-*table* fixture proves the coordinate space, (c) the
`pdf_user_space_v1` label in `xberg.go` is verified per page type.

## 4. What must not change

* ACK/TTL semantics, `result_version` content addressing, error codes, the
  Document IR schema version, the SQLite schema, the worker protocol.
* Defaults: `mode=fast` stays xberg+tesseract, `mode=accurate` stays docling-rs,
  OCR language stays `eng` until an operator or a per-deployment profile changes it.
* The CPU-only deployment must keep working with no GPU, no CUDA toolkit, no new
  model downloads.

## 5. PR split

| PR | Content | Risk | Gate |
| --- | --- | --- | --- |
| **A** | P1 flags + docs + unit tests | none (defaults unchanged) | unit, contract |
| **B** | P2 `ocr_quality_unknown` + escalation test | low (more escalations, bounded) | unit, lifecycle, benchmark latency |
| **C** | P3 docling-rs passthrough | none | unit |
| **D** | P5 refusal guard for `gpu_required` | low | unit + T12 unchanged |
| **E** | P4 encrypted documents | medium (API + redaction) | contract, security review |
| **F** | P6 IR OCR elements | blocked | upstream + fixture |

Order A → B → C → D. E on product decision. F when upstream delivers.

## 6. Configuration after PR-A…D (no API change)

| Knob | Default | Recommended for a Chinese audit corpus |
| --- | --- | --- |
| `--xberg-ocr-lang` | `eng` | `zh` (tesseract path) or leave `eng` when `paddle-ocr` is selected |
| `--xberg-ocr-backend` | *(unset → tesseract)* | `paddle-ocr` |
| `--xberg-ocr-backend-options` | unset | unset |
| `--docling-rs-ocr-mode` | *(unset → auto)* | unset; `full_page` only as an escape hatch |
| `--docling-rs-ocr-engine` | *(unset → ppocr)* | unset |
| `--worker-concurrency` | 2 | 2 (PP-OCR is ~3× tesseract per page, see benchmark) |
