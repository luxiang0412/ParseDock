# PR-B design — OCR escalation, revised

> Status: **implemented in PR-B** (`feat/ocr-pr-b-escalation`). This document is
> the revised design; the measured results are in `docs/history/OCR-PR-B-REPORT.md`.
>
> Inputs: `docs/history/OCR-BACKEND-RESEARCH.md`, `internal/engines/{xberg,escalate}.go`,
> `internal/worker/worker.go`, `tests/integration/ocr_backend.py`.

## 1. What was wrong

`internal/engines/xberg.go` emitted `ocr_low_confidence` for **every** OCR run:

```go
if out.Result.Metadata.OCRUsed || method == "ocr" {
    doc.AddWarning(documentir.WarnOCRLowConfidence,
        "OCR was used; text accuracy is lower than the native text layer", "info")
}
```

`ShouldEscalateToAccurate` consumed that code as evidence of poor quality and
added `WarnNoTextLayer` as a second trigger. Measured consequences (real
standalone process, real engines):

| Scenario | Engine | Text CER | CJK | Warnings |
| --- | --- | --- | --- | --- |
| default `eng`, `fast`, English scan | xberg | 0.000 | 0.00 | `ocr_low_confidence` |
| default, `auto`, Chinese scan (`eng`!) | xberg, **no escalation** | 0.453 | 0.00 | `ocr_low_confidence`, `degraded_conversion` |
| `paddle-ocr ch`, `fast`, Chinese scan | xberg | **0.000** | 0.61 | `ocr_low_confidence` |
| `paddle-ocr ch`, `auto`, Chinese scan | **docling-rs, escalated** | 0.002 | – | – |

So the trigger fired on a perfect result, and stayed silent on the worst one.
The label also lied to every consumer of the IR, not just the scheduler.

## 2. Three distinct semantics

| Code | Severity | Means | Can trigger escalation? |
| --- | --- | --- | --- |
| `ocr_used` (**new**) | info | the engine reports that OCR ran on ≥1 page | **no** |
| `ocr_quality_unknown` | info | OCR ran and the backend reported **no** confidence for any page (`score == null`) | **no** |
| `ocr_low_confidence` | warn | OCR ran **and** a page reported a score, and the minimum score is **< 0.60** | yes, with §3.2 |
| `no_text_layer` | warn | extraction produced **< 20 runes** on a PDF/image | yes |

`ocr_low_confidence` now carries the measured number in its message, e.g.
`OCR confidence 0.26 on page 1 (threshold 0.60)`. No adapter may invent a score;
paddle-ocr keeps returning `null` and is described as *unknown*, not *low*.

Severity matters: `info` never implies a defect. Downstream code that used
`ocr_low_confidence` as "OCR happened" must switch to `ocr_used`
(`docs/DOCUMENT-IR.md` updated).

## 3. The escalation rule

### 3.1 Gates (unchanged)

`mode` must be `auto` (never `fast`, never `accurate`), `format` must be
`pdf|png|jpeg|webp`, `--escalate-scans` must be on, and the accurate engine must
differ from the engine that produced the result. At most **one** escalation per
attempt — there is no loop.

`no_text_layer` is **not** "this is a scan": it is "extraction produced almost
nothing". A scan whose OCR succeeded does not carry it, so ordinary scans are not
re-parsed (requirement checked in §3.2).

### 3.2 Triggers

* **A — measured low confidence.** `ocr_low_confidence` (minimum page score
  below `0.60`), any script.
* **B — nothing was extracted.** `no_text_layer` (fewer than 20 runes on a
  PDF/image). Script-independent: a page with a stamp and no text has no language.

The first draft of this design gated trigger A on `CJKRatio >= 0.15`, on the
theory that docling-rs is only measurably better for CJK. Measurement rejected
that gate: a 90°-rotated Chinese page comes back as *Latin-looking garbage*
(CJK ratio 0.00) with a low score, and a rotated English page comes back empty
with no score at all — docling-rs recovers both exactly (CER 0.984/1.000 →
0.000). The gate would have created a blind spot in exchange for an unmeasured
saving, so the script ratio is now recorded in the routing reason for
auditability but no longer gates anything.

Explicitly **not** triggers: `ocr_used`, `ocr_quality_unknown`, `degraded_conversion`
(fires on `quality_score < 0.8`, which the wrong-language run also passes at 1.00),
and CJK ratio alone.

### 3.3 Accepting the retry — evidence, not size

The old rule was `res.TextLength() >= doc.TextLength()`, i.e. "more characters ⇒
better". That is a heuristic with no measurement behind it and it is removed. The
retry is now evaluated on each engine's **own quality evidence**:

| Condition | Decision |
| --- | --- |
| engine returned error / timeout / cancellation / lease loss | keep fast, record `rejected:error` |
| accurate result is empty (< 1 rune) | keep fast, record `rejected:empty` |
| accurate result carries `ocr_low_confidence` while the fast one did not | keep fast, record `rejected:alt_low_confidence(<score>)` |
| otherwise | accept, record `accepted` |

No claim of better accuracy is made anywhere: the IR records **what happened**
(trigger, score, engines, attempts, timings, decision), not a quality verdict.

### 3.4 Bookkeeping (requirement 7)

`routing.reason` becomes, e.g.:

```
ocr_quality_escalation: xberg -> docling-rs (trigger=low_confidence:0.26, attempts=2, decision=accepted, fast_ms=430, accurate_ms=1650)
ocr_quality_escalation: xberg -> docling-rs (trigger=no_text_layer, attempts=2, decision=rejected:alt_low_confidence(0.41), fast_ms=430, accurate_ms=900)
```

`metrics.parse_ms` is the sum of both engines (already the case, now asserted by a
test). `routing.fallback=true` marks the substitution. No new IR field, no schema
version change: everything fits in the existing `reason` string and `metrics`.

## 4. Threshold: 0.60, calibrated

Measured with tesseract `chi_sim` on the dense Chinese fixture and three degraded
variants of the same page (score = xberg's page `ocr_confidence.score`, CER =
page-1 gold):

| Variant (tesseract `chi_sim`, JSON path) | Score | CER | docling-rs CER | Escalate at 0.60? |
| --- | --- | --- | --- | --- |
| clean page | 0.84 | 0.09 | – | no |
| Gaussian blur | 0.87 | 0.06 | – | no (correct: result is fine) |
| salt/pepper noise | 0.85 | 0.08 | – | no (correct) |
| 6× downscale | 0.66 | 0.17 | **0.25** | no — and escalating would be *worse* |
| rotated 90° | **0.26** | 0.98 | **0.00** | **yes** (the retry fixes it completely) |
| rotated 180° | **0.31** | 0.93 | – | **yes** |
| English clean (`eng`) | 0.95 | 0.00 | – | no |
| English rotated 90° | **none** (no words) | 1.00 | **0.00** | **yes**, via trigger B |
| Chinese page read with `eng` (dense) | **0.51** | 0.45 | 0.002 | **yes** |
| Chinese page read with `eng` (sparse) | **0.38** | – | – | **yes** |
| English page read with `chi_sim` | **0.87** | 0.03 | – | **no** ← residual gap |

The pipeline itself confirmed the useful rows end to end: default (`eng`)
`auto` on a Chinese scan now escalates and lands at CER 0.002 (previously it
returned 0.453 untried), and a rotated page escalates once and lands at 0.000.

Reading it:

* 0.60 separates "text was recognised" (≥ 0.66) from "the page was garbled"
  (≤ 0.31) on the only degradation types where the score moves at all.
* the gap between 0.60 and 0.66 is deliberate: the 6× downscale case is a
  *moderate* failure where the retry is measurably **worse**, so the threshold
  must not reach it.
* **Known blind spots, stated not hidden**: (a) the score is insensitive to blur
  and noise (0.87 confidence at 0.06 CER, i.e. it cannot certify quality, only
  flag collapse); (b) **wrong-language OCR is only partly detectable** — a
  Chinese page read with `eng` loses confidence (0.38–0.51) and is now retried,
  but the reverse direction (English page read with `chi_sim`) stays at 0.87
  confidence and 3 % CER, so nothing fires; (c) **paddle-ocr reports no score at
  all**, so a garbled paddle result (measured: rotated page, CER 0.86) produces
  no trigger. PR-B narrows the wrong-language gap and does **not** close it; the
  mitigation stays configuration (`--xberg-ocr-backend paddle-ocr` reads both
  languages at CER 0.000) or one worker per language.

## 5. Tests

| Test | Type | Asserts |
| --- | --- | --- |
| `TestOCRUsedIsNotLowConfidence` | unit | `ocr_used` + `score=null` ⇒ `ocr_used` + `ocr_quality_unknown`, **no** `ocr_low_confidence` |
| `TestLowConfidenceNeedsAScore` | unit | score 0.42 ⇒ `ocr_low_confidence` with the number; score 0.85 ⇒ not |
| `TestNoTextLayerOnlyWhenAlmostEmpty` | unit | 12 runes ⇒ warn; 500 runes on a scan ⇒ not |
| `TestEscalateOnMeasuredLowScoreCJK` | unit | score 0.30 + CJK ⇒ escalate |
| `TestNoEscalationOnLowScoreLatin` | unit | score 0.30 + Latin ⇒ **no** escalation (no evidence of benefit) |
| `TestNoEscalationOnQualityUnknownAlone` | unit | `ocr_quality_unknown` with plenty of text ⇒ no |
| `TestNoEscalationOnOCRUsedAlone` | unit | `ocr_used` ⇒ no |
| `TestEscalationOnNoTextLayer` | unit | `no_text_layer` on a PDF ⇒ escalate, any script |
| `TestScannedPDFWithTextDoesNotEscalate` | unit | `ocr_used` + `ocr_quality_unknown` + 800 CJK runes ⇒ no |
| `TestEscalationRejectsLowConfidenceRetry` | worker | accurate result warns low confidence ⇒ fast kept, `rejected:alt_low_confidence` |
| `TestEscalationKeepsFastOnErrorTimeoutCancel` | worker | error/timeout/ctx-cancel ⇒ fast kept, no result mutation |
| `TestEscalationRecordsTimingAndDecision` | worker | `parse_ms` = fast+accurate, attempts=2, decision text present |
| `TestFastModeNeverEscalates` | unit | unchanged |
| `OCRB-03/04/07/08` | integration | paddle `auto` no longer escalates; default `eng` still not escalated (documented gap); rotated page escalates once and ends up correct; escalation failure keeps the fast text |

## 6. Out of scope

Public API, ACK/TTL, SQLite, IR schema, GPU routing and images are untouched.
`--xberg-ocr-backend` (PR-A) is the configuration-level mitigation for the
wrong-language gap.
