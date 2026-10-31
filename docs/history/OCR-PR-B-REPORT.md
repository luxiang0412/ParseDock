# PR-B report — OCR quality semantics and escalation

> Branch `feat/ocr-pr-b-escalation`, merged as the PR linked below.
> Design: `docs/history/OCR-PR-B-DESIGN.md`. Evidence: `tests/integration/ocr_backend.py`
> (9 real-process cases), Go unit tests, `benchmark/scripts/cer_breakdown.py`.
>
> No document from `test_data/` was read, copied or transmitted. All fixtures are
> synthetic and generated in-repo.

## 1. What changed

| Area | Before | After |
| --- | --- | --- |
| `ocr_used` | did not exist; `ocr_low_confidence` was used to mean "OCR ran" | own code, severity `info`, emitted whenever an engine reports OCR |
| `ocr_quality_unknown` | defined, never emitted | emitted when OCR ran but **no** page reported a confidence (PP-OCRv6) |
| `ocr_low_confidence` | emitted for **every** OCR run with a message about accuracy | only when a page score is **below 0.60**; the message quotes score, page and threshold |
| `no_text_layer` | only docling-rs, `< 20` chars | same rule in xberg too; it means *extraction failed*, not *the input is a scan* |
| Escalation trigger | any OCR warning + `CJKRatio ≥ 0.15` | measured low confidence (any script) or `< 20` chars extracted |
| Retry acceptance | `accurate.TextLength() ≥ fast.TextLength()` (more characters ⇒ better) | rejected on error/timeout/cancel/empty, or when the retry itself reports low confidence; otherwise accepted |
| Bookkeeping | one-line reason, summed `parse_ms` | trigger+score+page, attempts, decision, both engines, both durations, CJK ratio; rejected retries still charge their time |
| IR | `Document` had no in-memory OCR evidence | `Document.OCR *OCRQuality` (`json:"-"`, not serialised, no schema change) |

`mode=fast` and `mode=accurate` never escalate; `auto` escalates at most once
(the code path is single-shot by construction).

## 2. Design changes forced by measurement

Three parts of the earlier design did not survive contact with the code and the
measurements, and were revised rather than shipped:

1. **The CJK gate on low confidence was removed.** The draft required
   `CJKRatio ≥ 0.15` to avoid retrying Latin pages. But a 90°-rotated Chinese page
   returns *Latin-looking garbage* (CJK ratio 0.00, score 0.26–0.41) and a rotated
   English page returns nothing (score absent, CER 1.00); docling-rs recovers both
   to CER 0.000. The gate created a blind spot in exchange for an unmeasured
   saving. The ratio is still recorded in the reason string.
2. **The "below 0.40 escalates in any script" rule was dropped.** It was a policy
   with no calibration behind it. The single 0.60 threshold now has a measured
   table (§3), including a case where escalating would be *worse*.
3. **The "unknown confidence + fewer than 40 chars/page" rule was dropped** in
   favour of the existing `no_text_layer` code with its long-standing 20-character
   threshold. Two thresholds for the same phenomenon is how vocabularies rot.

## 3. Threshold calibration (0.60)

Tesseract `chi_sim` on the dense Chinese page and five degradations, JSON path
(page-1 gold):

| Variant | Score | CER | docling-rs CER | Escalates? |
| --- | --- | --- | --- | --- |
| clean | 0.84 | 0.09 | – | no |
| Gaussian blur | 0.87 | 0.06 | – | no (result is fine) |
| salt/pepper noise | 0.85 | 0.08 | – | no (result is fine) |
| 6× downscale | 0.66 | 0.17 | **0.25** | no — escalating would be *worse* |
| rotated 90° | **0.26** | 0.98 | **0.00** | **yes** |
| rotated 180° | **0.31** | 0.93 | – | **yes** |
| English clean (`eng`) | 0.95 | 0.00 | – | no |
| English rotated 90° | **none** | 1.00 | 0.00 | **yes** (via `no_text_layer`) |

0.60 is the only value that separates "the page was garbled" from "the page was
read", and it deliberately stays below the 0.66 case where a retry measurably
regresses. The gap to 0.66 is 0.06 — small, and stated as such.

## 4. Results

### 4.1 Unit tests (Go)

| Test | Asserts |
| --- | --- |
| `TestOCRUsedIsNotLowConfidence` | `ocr_used` + `score=null` ⇒ `ocr_used` + `ocr_quality_unknown`, never `ocr_low_confidence` |
| `TestLowConfidenceNeedsAScore` | 0.42 ⇒ low (message quotes 0.42, severity `warn`); 0.85 ⇒ not |
| `TestLowConfidenceUsesTheWorstPage` | one bad page among good ones is not averaged away |
| `TestNoTextLayerOnlyWhenAlmostEmpty` | 5 runes on a PDF ⇒ warn; 500 runes ⇒ not; short **txt** ⇒ not |
| `TestNoOCRMeansNoOCRWarning` | OCR off ⇒ no OCR warnings at all |
| `TestEscalationTriggers` (12 cases) | low score any script ⇒ yes; unknown ⇒ no; `ocr_used` alone ⇒ no; `<20` chars ⇒ yes; plenty of text ⇒ no; `fast`/`accurate` ⇒ no; `docx` ⇒ no |
| `TestNoTextLayerIsAFailureSignalNotAScanMarker` | a scan whose OCR produced text is never re-parsed |
| `TestEscalationReasonIsAuditable` | trigger, attempts, decision, both durations, CJK ratio, and the rejection detail are all present |
| worker: `TestEscalationUsesAccurateEngine` | accepted path: engine swapped, `fallback=true`, reason fields, `parse_ms` = 10+40 |
| worker: `TestEscalationRejectsLowConfidenceRetry` | retry that still scores 0.41 is rejected, reason `alt_low_confidence(0.41)`, rejection time charged (30+90) |
| worker: `TestEscalationRejectsEmptyRetry` | empty retry rejected |
| worker: `TestEscalationFailureKeepsFastResult` | error / deadline / cancelled: fast result kept, text untouched, `decision=rejected:error` |
| worker: `TestNoEscalationOnUnknownQuality` | PP-OCRv6-style result is not re-parsed; parse time unchanged |
| worker: `TestEscalationDisabledAndExplicitModes` | disabled, `fast`, `accurate` all stay put |

### 4.2 Real-process integration (`tests/integration/ocr_backend.py`, 9/9 passing)

| Case | Outcome |
| --- | --- |
| OCRB-01 default `fast`, English scan | `['ocr_used']`, CER 0.000 — no false low-confidence |
| OCRB-02 `paddle-ocr`, `fast`, Chinese scan | `['ocr_used','ocr_quality_unknown']`, CER 0.000 |
| OCRB-03 `paddle-ocr`, `auto`, Chinese scan | **no escalation**, engine stays `xberg`, CER 0.000 ← the defect this PR fixes |
| OCRB-04 default `eng`, `auto`, Chinese scan | **now escalates** (score 0.38–0.51), final CER 0.002 (was 0.453, untried) |
| OCRB-05 unknown backend | rejected at startup, exit 1 |
| OCRB-06 runtime model writes | none |
| OCRB-07 rotated page, `auto` | escalates once, `trigger=low_confidence:0.41@1, attempts=2, decision=accepted, fast_ms=475, accurate_ms=1443`, final CER **0.000** |
| OCRB-08 rotated page on `paddle-ocr` | no score ⇒ no trigger, CER 0.858 — **known gap**, recorded |
| OCRB-09 `chi_sim` on an English page | confident (0.87) ⇒ no trigger, CER 0.033 — **residual gap**, recorded |

## 5. Quality risks that remain

1. **Paddle-ocr's silence is a blind spot.** It reports no confidence, so a
   garbled PP-OCRv6 page is never retried (OCRB-08: CER 0.858 on a rotated page).
   Fixing this needs either a signal from the backend (upstream) or a
   script-independent structural check — explicitly *not* invented here.
2. **The wrong-language gap is narrowed, not closed.** `eng` on Chinese now loses
   confidence and is retried, but `chi_sim` on English stays at 0.87 and is not
   retried (3 % CER). Configuration (`--xberg-ocr-backend paddle-ocr`) or one
   worker per language is the real mitigation.
3. **The score cannot certify quality.** Blur and noise leave it at 0.85–0.87
   while CER stays at 0.06–0.08 — that is fine here, but it means "no
   `ocr_low_confidence` warning" must never be read as "the text is correct".
4. **0.60 is calibrated on one font, one page, one engine version.** The table is
   reproducible (`benchmark/scripts/cer_breakdown.py`), and a real gold set should
   re-tune it.

## 6. Definition of Done

| Requirement | Status |
| --- | --- |
| 1. Three independent semantics | ✅ §1, tests §4.1 |
| 2. `ocr_low_confidence` only with verified evidence | ✅ score < 0.60 and quoted in the message |
| 3. `no_text_layer` is not "this is a scan" | ✅ `< 20` chars only; scan-with-text test |
| 4. `ShouldEscalateToAccurate` redesigned; fast never, auto once | ✅ 12 cases + worker tests |
| 5. Thresholds reviewed with evidence, scope and misjudgements | ✅ §3, §5 |
| 6. No "more characters / fewer warnings ⇒ better" claims | ✅ length rule removed; acceptance uses the retry's own evidence; no accuracy claim anywhere |
| 7. Retry failure keeps the original; timing/attempts/engine/reason recorded | ✅ reason string, `parse_ms`, rejected retries charged |
| 8. Four real scenarios regress-tested end to end | ✅ OCRB-01..04 (+07..09) |
| 9. Wrong-language gap documented, not claimed solved | ✅ §5.2, `docs/DEPLOYMENT.md` |
| 10. Defaults, public API, ACK/TTL, SQLite, IR schema, CPU-only untouched | ✅ no API/DB change; IR schema file unchanged (`json:"-"` field); no new dependency |

## 7. Follow-ups (PR-D and beyond)

1. **PR-D stage 1** as designed: `--gpu-evidence=warn`, per-engine `GPUEvidence`,
   metric + IR warning, no routing change. The MinerU `libvulkan` finding makes
   stage 2 (`require`) contingent on a rebuilt image.
2. **`--xberg-ocr-auto-rotate`** is a cheap, measured win for the tesseract path
   (rot90 CER 0.984 → 0.093, one flag that already exists in xberg 1.3.10). It
   changes output, so it needs its own opt-in flag and benchmark, not a silent
   default.
3. **Rotation for the paddle path** is unsolved (OCRB-08). Before adding
   heuristics, check whether a newer xberg/paddle build exposes an orientation
   signal.
4. **Re-tune 0.60** against a real, hand-transcribed sample once one exists.
