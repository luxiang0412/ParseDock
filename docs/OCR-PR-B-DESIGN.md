# PR-B design — OCR escalation, revised

> Status: **design + evidence only**. No production behaviour changed in this
> round; PR-A (`--xberg-ocr-backend`) is the only code that shipped.
>
> Evidence: `tests/integration/ocr_backend.py` (real processes, real engines),
> `docs/OCR-BACKEND-RESEARCH.md` §6, `docs/OCR-GPU-BENCHMARK.md` §2.2.

## 1. What the code does today

`internal/engines/xberg.go` (any OCR run):

```go
if out.Result.Metadata.OCRUsed || method == "ocr" {
    doc.AddWarning(documentir.WarnOCRLowConfidence,
        "OCR was used; text accuracy is lower than the native text layer", "info")
}
```

`internal/engines/escalate.go`:

```go
if doc == nil || !hasOCRWarning(doc) { return false }   // WarnOCRLowConfidence | WarnNoTextLayer
return CJKRatio(doc) >= 0.15
```

`internal/coordinator/service.go` runs the accurate engine once when
`mode=auto && ShouldEscalateToAccurate(...)`.

So the warning code means "OCR was used", not "confidence was low", and the
escalation trigger consumes it as if it meant the latter. Three consequences, all
measured end to end (§3).

## 2. The three concepts are currently collapsed into one warning

| Concept | What it should mean | What exists today |
| --- | --- | --- |
| `ocr_used` | OCR ran on at least one page. **Never** a quality signal. | no distinct code; `WarnOCRLowConfidence` is emitted with severity `info` as a stand-in |
| `ocr_low_confidence` | the engine **measured** a confidence below a threshold | emitted unconditionally, including for a CER-0.000 paddle-ocr result |
| `ocr_quality_unknown` | OCR ran but the backend reports **no** confidence | defined in `documentir` and never emitted by any adapter |

Severity cannot carry the distinction either: the note is emitted at severity
`info`, and nothing downstream reads severity, only the code.

## 3. Evidence

`tests/integration/ocr_backend.py`, 2026-10-31, real standalone process:

| Case | Engine finally used | Text CER | CJK ratio | Warnings in the IR |
| --- | --- | --- | --- | --- |
| default (`tesseract eng`), `fast`, English scan | xberg | 0.000 | 0.00 | `['ocr_low_confidence']` |
| default, `auto`, Chinese scan | **xberg (no escalation)** | **0.453** | **0.00** | `['ocr_low_confidence','degraded_conversion']` |
| `paddle-ocr ch`, `fast`, Chinese scan | xberg | **0.000** | 0.61 | `['ocr_low_confidence']` |
| `paddle-ocr ch`, `auto`, Chinese scan | **docling-rs (escalated)** | 0.002 | – | `[]` |

Four facts follow.

1. **A perfect result is labelled "low confidence"** (row 3). The warning is
   semantically wrong, and any consumer — including the escalation trigger, a
   dashboard, or a reviewer — is misled by it.
2. **A perfect result is re-parsed** (row 4). PP-OCRv6 at CER 0.000 triggers a
   full docling-rs run (~1.65 s and 1.4 GiB in the benchmark) and returns CER
   0.002 — *worse* than what it replaced, for double the cost.
3. **The case that most needs escalation does not get it** (row 2). A Chinese
   scan read with `eng` yields Latin garbage: 345 substitutions, 84 insertions,
   `CJKRatio = 0.00`, no escalation. This is G5 in the routing proposal, now
   confirmed end to end.
4. **`ocr_quality_unknown` alone cannot fix row 2.** It fires when a backend
   reports no confidence — which is exactly the paddle-ocr case (row 3/4), the
   one that should *not* escalate. Emitting it and escalating on it would simply
   move the redundant re-parse from tesseract to paddle-ocr.

### 3.1 Why "no confidence" must not mean "bad"

The whole point of PP-OCRv6 in this stack is that it returns `score = null`
(`ocr_confidence.word_count = 0`). If unknown ⇒ escalate, then every Chinese
scan in every `auto` job is parsed twice, forever. That is the opposite of the
intent.

### 3.2 What can actually detect row 2

Nothing in-band, from a single pass, with certainty:

* the extracted text is plausible Latin — decided *by the wrong model*;
* `quality_score` stays 1.00 (verified: the wrong-language run scored 1.00);
* `scanned_confidence` only says "this is a scan", which is true either way.

The honest options are (a) configure the OCR language to match the corpus, or
(b) make the backend language-agnostic — which is what `paddle-ocr` is (CER 0.000
on both fixtures with `ch`). Detection is a *second* pass in another language,
which is exactly what `accurate` escalation gives you, so:

**PR-B triggers escalation on structural evidence, not on `CJKRatio` of
garbage text, and PR-A's `paddle-ocr` opt-in is the real fix for the language
trap.** The detection gap is stated, not papered over.

## 4. Revised design

### 4.1 New warning taxonomy (`internal/documentir`)

| Code | Severity | Emitted when | Consumed by escalation? |
| --- | --- | --- | --- |
| `ocr_used` (new constant, existing string `"ocr_used"`) | `info` | the engine reports `ocr_used`/`extraction_method=ocr` for ≥1 page | **no** |
| `ocr_quality_unknown` | `info` | OCR ran and the backend reported no usable confidence (`score == null`, `word_count == 0`) | only together with §4.2(c) |
| `ocr_low_confidence` | `warn` | OCR ran **and** `score != null && score < 0.60` | yes |
| `ocr_not_run` | `info` | unchanged (already defined) | no |

Threshold 0.60: tesseract's page score on the clean Chinese fixture is 0.85 and
on the English one 0.85; 0.60 leaves room for a real-world dip without firing on
a good scan. The constant lives in `internal/engines` next to `CJKRatio`.

**No backend may ever be forced to invent a score.** paddle-ocr keeps returning
`null`; we just stop calling that "low".

### 4.2 Escalation rule

```go
func ShouldEscalateToAccurate(mode, format string, doc *documentir.Document) bool
```

escalates when **OCR ran** and any of:

* **(a)** `ocr_low_confidence` — a measured score below 0.60, and
  `CJKRatio >= 0.15` (language where docling-rs measurably wins) *or* the score is
  below 0.40 regardless of script (a 40 %-confident page is worth one retry in
  any language);
* **(b)** `no_text_layer` on an image-only page (today's behaviour, kept);
* **(c)** `ocr_quality_unknown` **and** the result is structurally thin — fewer
  than `40` characters per OCR'd page. This catches the empty/garbage scan
  (nothing to say after OCR) without touching a good PP-OCRv6 result;
* **(d)** `mode=accurate` never escalates (it is already the accurate engine) and
  `mode=fast` never escalates (a `fast` request must stay fast) — unchanged.

Explicitly **not** a trigger: `ocr_used` on its own, `ocr_quality_unknown` on its
own, `CJKRatio` on its own, and any `degraded_conversion` warning (that fires on
`quality_score < 0.8`, which the wrong-language run also passes at 1.00 — see
§3.2 — so it is not evidence of OCR trouble).

### 4.3 The duplicated-parse guard

Even with a correct trigger, `auto` must not pay twice for the same bytes:

* the escalation gets a `routing.reason` prefix `ocr_quality_escalation:` (already
  the case) and one escalation maximum (already the case);
* **new**: if the accurate engine's result is *worse* than the fast one, keep the
  original. Compare with the same predicates we can afford: (i) both produced
  text and the accurate result has fewer OCR warnings, or (ii) the accurate
  result has ≥90 % of the fast result's characters. Otherwise keep fast and
  record `escalation_rejected` in `routing.reason`. This is the guard that would
  have prevented row 4 from replacing CER 0.000 with CER 0.002.

### 4.4 What is *not* in PR-B

* No new public API field, no error-code change, no IR schema version bump (the
  warning constants already exist; `ocr_used` is a new constant with an existing
  vocabulary string).
* No change to `mode=fast` or `mode=accurate` routing, ACK/TTL, or the SQLite
  schema.
* No confidence thresholds learned from a real corpus — 0.60/0.40 are documented
  constants to be re-tuned when a real gold set exists.

## 5. Tests for PR-B

| Test | Type | Asserts |
| --- | --- | --- |
| `TestXbergWarningOCRUsedNotLowConfidence` | unit | `ocr_used=true, score=null` ⇒ `ocr_used` + `ocr_quality_unknown`, **no** `ocr_low_confidence` |
| `TestXbergWarningLowConfidenceWhenScored` | unit | `score=0.42` ⇒ `ocr_low_confidence`; `score=0.85` ⇒ not |
| `TestEscalateOnMeasuredLowScore` | unit | score 0.40 on a CJK doc ⇒ escalate |
| `TestEscalateLowScoreAnyScriptBelow040` | unit | score 0.30 on a Latin doc ⇒ escalate |
| `TestNoEscalationOnQualityUnknownWithDenseText` | unit | `ocr_quality_unknown` + 1 200 chars ⇒ **no** escalation (this is today's row 4) |
| `TestEscalationOnQualityUnknownWithThinText` | unit | `ocr_quality_unknown` + 12 chars on a scanned page ⇒ escalate |
| `TestNoEscalationOnOCRUsedOnly` | unit | `ocr_used` alone ⇒ no escalation |
| `TestEscalationKeepsBetterResult` | coordinator | escalation result with fewer chars / more warnings ⇒ original kept, `escalation_rejected` recorded |
| `TestEscalationSingleShot` | coordinator | at most one escalation per job (existing behaviour preserved) |
| `OCRB-03` (integration) | real engines | paddle-ocr `auto` on a Chinese scan ⇒ **engine stays xberg**, CER ≤ 0.02, warnings contain `ocr_used` and not `ocr_low_confidence` |
| `OCRB-04` (integration) | real engines | default `eng` on a Chinese scan ⇒ unchanged: still no escalation (documented gap), but the warnings now describe reality |

The two integration cases flip from "record the wrong behaviour" to "assert the
right one", which is why PR-B must land before anyone recommends `auto` on a
mixed corpus.

## 6. Risk and rollback

| Risk | Mitigation |
| --- | --- |
| Fewer escalations ⇒ worse output on real CJK scans | the rule keeps every trigger that has measured evidence; the `ocr_used`-only trigger was never justified by a measurement |
| The 0.60/0.40 thresholds are guesses | constants in one place, documented as such, re-tuned against a real gold set; a deployment can raise them via config in a follow-up if needed |
| `escalation_rejected` surprises an operator | it is recorded in the human-readable `routing.reason`; no new API shape |
| Dashboard/consumers keyed on `ocr_low_confidence` | the code keeps its meaning for genuinely low scores; consumers that used it as "OCR happened" must switch to `ocr_used` — noted in `docs/DOCUMENT-IR.md` |

Rollback is a one-line revert of the trigger predicate; the warning split is
additive.
