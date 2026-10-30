# ENGINE DECISION — Phase 0 outcome

> Inputs: `docs/ENGINE-BENCHMARK.md` (measured), `docs/LICENSE-REVIEW.md`
> (verified licences), `docs/ENGINE-CANDIDATES.md` (behaviour).
> Rubric and weights were fixed **before** the numbers were known.

## 1. Decision

Ship two engines in v0.1 and route by `mode`:

| Role | Engine | Why |
| --- | --- | --- |
| **fast** | **xberg 1.3.10** (subprocess) | 10–25 ms for native PDF/DOCX/XLSX, 220–700 ms when OCR is needed, 21–39 MB RSS, table grid + bbox |
| **accurate** | **docling-rs 2.53.3** (subprocess) | best measured CER (0.007 en / 0.034 zh), full body-tree reading order, per-element bbox with declared coordinate origin, table spans, XLSX sheet names |
| text formats | **native (Go)** | < 1 ms, correct GB18030/UTF-16 decoding, no model load |
| fallback (opt-in) | **Apache Tika 4.2.0** | broadest coverage; text-only quality |
| optional GPU | **MinerU 4.1.2 `flash`** | local-only GPU OCR; structured IR is weaker (no bbox) |

Explicitly **not** in the first version: docling-serve (2 s/request overhead and a
7.2 GB runtime; kept as an optional backend and as the Docling conformance oracle),
MinerU's remote tiers (document upload to a third party), and no engine at all in
the coordinator process.

## 2. Weighted scoring (arithmetic shown)

Weights: quality 40 %, performance 25 %, resources 15 %, format coverage 10 %,
ops/licence 10 %. Dimension scores are 0–10 as defined in
`ENGINE-BENCHMARK.md` §7.

### Quality

`quality = 8 × (1 − mean CER) + 2 × table_accuracy`, scans double-weighted in the mean.

* **docling-rs** — CERs used: en 0.0072, zh 0.0337, two-column 0.2772, scan-en 0.8836,
  scan-zh 0.098, CJK image 0.098, docx 0.284 →
  mean = (0.0072+0.0337+0.2772+0.284 + 2×0.8836 + 2×0.098)/7 = **0.367**;
  quality = 8×(1−0.367) + 2×1.0 = 5.06 + 2.0 = **7.1**.
* **xberg** — en 0.1157, zh 0.4398, two-column 0.2865, docx 0.082, scan-en 0.0548,
  scan-zh 0.8235, CJK image 0.8431 →
  mean = (0.1157+0.4398+0.2865+0.082 + 2×0.0548 + 2×0.8235)/7 = **0.383**;
  quality = 8×0.617 + 2×1.0 = **6.9**.
* **docling-serve** — same as docling-rs for native text, but scans return nothing:
  mean = (0.006+0.0337+0.2772+0.284 + 2×1.0 + 2×1.0)/7 = **0.657**;
  quality = 8×0.343 + 2×1.0 = **4.7** (OCR-off default punishes it; with OCR on it
  would match docling-rs at 4× the latency).
* **MinerU flash** — en 0.1666, zh 0.6276, two-column 0.2759, scan-en 0.6644,
  scan-zh 0.451 (no docx/CJK image) → mean = (0.1666+0.6276+0.2759 +
  2×0.6644 + 2×0.451)/6 = **0.550**; quality = 8×0.45 + 2×1.0 = **5.6**.
* **Tika** — en 0.1233, zh 0.5104, two-column 0.2958, scan-en 0.1986, scan-zh 1.2549,
  CJK image 0.9412, docx 0.045 → mean = (0.1233+0.5104+0.2958+0.045 +
  2×0.1986 + 2×(1.2549+0.9412)/2)/7 = **0.510**; no table structure → quality =
  8×0.490 + 0 = **3.9**.

### Final table

| engine | quality (40 %) | performance (25 %) | resources (15 %) | coverage (10 %) | ops/licence (10 %) | **weighted** |
| --- | --- | --- | --- | --- | --- | --- |
| **xberg** | 6.9 | 9.5 | 10.0 | 9.5 | 6.0 | **8.2** |
| **docling-rs** | 7.1 | 3.0 | 6.0 | 9.0 | 9.0 | **6.3** |
| Tika | 3.9 | 7.0 | 6.0 | 10.0 | 7.0 | **5.8** |
| MinerU flash | 5.6 | 5.0 | 7.0 | 4.0 | 5.0 | **5.4** |
| docling-serve | 4.7 | 1.5 | 4.0 | 8.0 | 8.0 | **4.5** |

Derivation of the non-quality dimensions:

* **Performance** — docling-rs p95 820 ms/doc (3 p) and 680 ms (1 p scan) → 3.0;
  xberg 23 ms / 226 ms → 9.5 (capped below 10 because OCR is still ~0.2–0.7 s);
  Tika 12 ms / 347 ms → 7.0 (unpredictable parser init under load); MinerU 0.9 s
  → 5.0; docling-serve 2.0 s flat → 1.5.
* **Resources** — xberg 39 MB median / 232 MB peak → 10; docling-rs 818 MB median /
  1 167 MB peak → 6 (fits a 1.5 GB worker with care); MinerU 54 MB CLI + ~880 MB
  resident VRAM → 7; Tika: 1.37 GB image, JVM heap shared → 6; docling-serve 7.2 GB
  runtime → 4.
* **Coverage** — of the 11 target formats: xberg 10.5 (no sheet names), docling-rs 10
  (no proprietary `.doc/.xls`), Tika 11 (but text only), docling-serve 9 (no
  csv/tsv), MinerU 4 (pdf/images).
* **Ops/licence** — docling-rs: one binary + pinned model bundle, MIT → 9;
  xberg: MIT core but the release tarball bundles `libx265` (GPL) and needs glibc ≥ 2.38
  → 6; Tika: JVM + moving `latest-full` tag → 7; MinerU: daemon + telemetry + licence
  addendum + AGPL weights elsewhere → 5; docling-serve: Python env → 8.

**Interpretation.** No single engine wins on both axes, and the weighted total
(xberg 8.2, docling-rs 6.3) is dominated by the resource weight — which is exactly
why the architecture routes per request instead of picking one engine. The
`mode=accurate` path accepts 3–4× lower throughput for 2–3× lower CER and full
structure; `mode=fast` trades structure for 30–80× latency improvement.

## 3. Routing table as implemented

`EngineSpec` priorities in `internal/engines/capabilities.go`, used by both the
coordinator (routing decision + `routing_reason`) and the worker (engine pick,
deterministic):

| format | mode=fast | mode=accurate | mode=auto |
| --- | --- | --- | --- |
| txt, md, csv, tsv | native (100) | xberg (50)¹ | native |
| pdf | xberg (50) | docling-rs (40) | xberg |
| docx, pptx | xberg (50) | docling-rs (40) | xberg |
| xlsx | xberg (50) | docling-rs (40) | xberg |
| png, jpeg, webp | xberg + OCR (50) | docling-rs + OCR (40) | xberg |
| anything with `device=gpu_required` | – | MinerU/docling-serve if a GPU worker is registered, else `409 gpu_unavailable` | same |

¹ `mode=accurate` on a CSV is served by xberg because the native engine only
declares `fast`; the routing reason records this.

`mode=accurate` never falls back to an `auto`-only engine, and `gpu_required` never
falls back to CPU (both covered by unit tests in
`internal/coordinator/service_test.go::TestRouteModeSemantics` and T12).

## 4. P0 hard gates

| Gate | xberg | docling-rs | docling-serve | MinerU flash | Tika |
| --- | --- | --- | --- | --- | --- |
| Licence suitable for a service deployment | ⚠️ GPL helper lib decision (L1) | ✅ | ✅ | ⚠️ attribution + thresholds | ✅ |
| Core formats usable | ✅ | ✅ | ✅ | ⚠️ pdf/images only | ✅ (text) |
| Runs CPU-only | ✅ | ✅ | ✅ | ✅ | ✅ |
| Reproducible container | ✅ (1.19 GB image) | ✅ | ⚠️ (7.2 GB venv) | ⚠️ needs CUDA base | ✅ pinned digest |
| Adversarial input bounded | ✅ (ParseDock pre-flight) | ✅ | ✅ | ✅ | ❌ Tika accepted the zip bomb |
| **Passed** | ✅ conditional on L1 | ✅ | ✅ | ⚠️ optional only | ✅ fallback only |

## 5. Risk register for the selected pair

| Risk | Impact | Mitigation | Trigger to revisit |
| --- | --- | --- | --- |
| xberg bundles `libx265` (GPL-2.0) | cannot ship the tarball as-is | rebuild without the HEVC encoder, or ship the source offer (L1) | legal sign-off blocked, or upstream changes the bundle |
| docling-rs memory (0.8–1.2 GB) | worker OOM on small nodes | cgroup 1.5 GB per worker, `--worker-concurrency=1` for accurate | OOM kills in production |
| docling-rs OCR quality on synthetic scans (CER 0.88 EN) | bad text for low-quality scans | route scans to MinerU/xberg OCR, or fix rasterisation; add OCR-specific fixtures | corpus shows > 0.3 CER on real scans |
| docling-rs has no GPU build | accurate path is CPU-bound | keep GPU for OCR-heavy work (MinerU), revisit CUDA-enabled docling builds | GPU queue grows |
| Two engines = two supply chains | patching/upgrades double | pin digests, one manifest, conformance suite per engine | maintenance cost > 1 engineer-week/month |
| MinerU daemon + AGPL weights | licence/complexity | flash-only, egress blocked, separate optional image | MinerU becomes required for quality |

## 6. Exit conditions for the selected engines

An engine is removed from v0.1 if any of the following is observed on the expanded
corpus (PR-11) or in production for a month:

1. licence cannot be resolved (xberg L1) → replace with docling-rs only, or rebuild;
2. relative quality regression > 10 % that is not fixed within a release;
3. p95 latency regression > 50 % on the same hardware without a compensating gain;
4. a crash/OOM/hang class of failures that cannot be bounded by the worker;
5. format coverage gap that forces a third engine into the hot path.

## 7. Consequences for the product

* Clients do **not** choose engines; they choose `mode`/`device`. Swapping an engine
  is a coordinator config change, not an API break.
* Results always state which engine and version produced them
  (`parser_engine`, `parser_version`, `routing.reason`), so quality regressions are
  attributable.
* The IR, not the engine, is the contract: docling-rs's richer structure is
  normalised into `doc-ir/0.1`, and the gaps (no confidence, no formula text) are
  declared as warnings rather than silently patched.
