# DOCUMENT IR v0.1

Schema id: `doc-ir/0.1` (const `documentir.SchemaVersion`).

**Normative JSON Schema:** `internal/documentir/schema/document-ir-0.1.schema.json`,
published as `api/document-ir-0.1.schema.json` by `make schema`. CI validates
representative IRs against it (`internal/documentir/schema_test.go`), fails when the
published copy drifts, and pins the Docling→IR mapping with a golden file
(`internal/engines/testdata/docling_sample.golden.json`, refresh with
`UPDATE_GOLDEN=1`).
Implemented in `internal/documentir/ir.go`; renderers in `export.go`.
JSON is the IR itself; Markdown and Text are **derived views**, never the source of truth.

## Redundant table text is dropped

Engines commonly return a table twice: as the lines of text on the page and as a structured
`Table`. Keeping both made every rendered Markdown/text output repeat the cells. The worker now
calls `DropTableShadows()` after parsing: the run of paragraphs immediately preceding a table
element is removed when, and only when, it matches the table's rows one per row in order. A
paragraph elsewhere that happens to quote a row is untouched, and a partially matching run is
left alone (prose is more likely than a broken table).

## 1. Why not "just Markdown"

* Markdown loses page numbers, coordinates, sheet names, cell spans, reading order and confidence.
* Different engines emit different Markdown dialects; storing that would freeze one engine's quirks
  into the contract.
* Downstream (Chat citations, RAG metadata, audit trails) needs `page`/`bbox`/`sheet` provenance.

Therefore ParseDock stores a structured IR and renders three views. The Markdown view is
CommonMark + GFM: headings, numbered and bulleted lists (numbers and nesting kept), `>`
quotes, fenced code with its language, pipe tables (HTML tables when cells are merged) and
inline formatting from `rich_text`; plain text is escaped only where Markdown would
otherwise reinterpret it (a literal `*`, a line starting with `1.` or `#`). Page furniture
is skipped in Markdown and text and kept in JSON.

| View | Endpoint | Content-Type | Use |
| --- | --- | --- | --- |
| `json` | `/v1/jobs/{id}/result?format=json` | `application/json` | canonical, full provenance |
| `markdown` | `…?format=markdown` (default) | `text/markdown` | LLM prompts, humans |
| `text` | `…?format=text` | `text/plain` | embeddings/legacy pipelines |

## 2. Structure

```jsonc
{
  "schema_version": "doc-ir/0.1",
  "source":   { "file_name", "mime_type", "format", "size_bytes", "sha256" },
  "parser_engine": "docling-rs",
  "parser_version": "docling-rs 2.53.3 (chunking, mcp); ONNX Runtime 1.31",
  "routing":  { "requested_mode", "requested_device", "actual_device",
                "engine", "fallback", "reason" },   // actual_device: gpu|cpu|unknown
  "page_count": 3,
  "page_kind": "pdf" | "sheet" | "slide" | "none",
  "elements": [ Element… ],          // reading order = array order (reading_order field duplicated)
  "tables":   [ Table… ],            // referenced from elements[].table_id
  "warnings": [ Warning… ],
  "metrics":  { "parse_ms", "input_bytes", "output_chars", "pages",
                "peak_rss_mb"?, "peak_vram_mb"?, "model_load_ms"?,
                "started_at"?, "finished_at"? }
}
```

### Routing.actual_device

`actual_device` is the device that **executed this task**, derived from evidence,
not from the engine's declared capability:

| Value | Meaning |
| --- | --- |
| `gpu` | the adapter proved GPU execution for this task; only this value satisfies `device=gpu_required` |
| `cpu` | the adapter proved CPU execution (CPU-only adapter, engine default, or an observed provider fallback) |
| `unknown` | nothing could be proven — never read as "GPU", and not as "CPU" either |

On GPU workers `routing.reason` ends with `; exec=<kind>(<source>)`, e.g.
`exec=cpu_verified(mineru:daemon-log-startup)`.

### Element

```jsonc
{
  "id": "el_2",
  "type": "title|section|paragraph|list|list_item|table|image|formula|caption|footnote|header|footer|code|page_break|unknown",
  "text": "…",                       // plain text: inline markup stripped
  "rich_text": "… **bold** …",       // optional: same content with CommonMark inline markup
  "level": 2,                        // heading level or list nesting; 0/absent otherwise
  "reading_order": 1,
  "confidence": 0.93,                // only when the engine actually reports it
  "source_location": { … },
  "table_id": "tbl_1",               // when type=table
  "image_ref": "",                    // RESERVED: no adapter emits one yet (see §6)
  "metadata": { "content_layer": "furniture" }
}
```

**`text` vs `rich_text`.** `text` is always plain, so search, embeddings, the `text` view
and the benchmark scoring (gold is plain text) are unaffected by formatting. `rich_text`
(also on table cells) carries the same words with CommonMark inline markup — `**bold**`,
`*italic*`, `` `code` ``, `[text](url)`, `~~strike~~` — and is what the Markdown view
renders. It is **absent** when the engine reported no inline formatting (or the
markup-free text is identical); consumers then use `text`. It is never invented: it comes
from the engine's own Markdown (xberg, MinerU, native `.md` input) or from Docling's
per-run `formatting`/`hyperlink` fields. Underline and sub/superscript have no CommonMark
form and are dropped from `rich_text` (the words stay). `rich_text` counts against the same
text budget as `text` (`Limits.MaxTextBytes`).

**Element metadata keys with a defined meaning** (all values are strings; unknown keys are
passed through):

| Key | Values | Set by | Rendered as (Markdown) |
| --- | --- | --- | --- |
| `content_layer` | `furniture` | Docling `content_layer`; page headers/footers | skipped in Markdown **and** text; kept in JSON |
| `markdown` | `blockquote` | Markdown `>` lines | `> ` quote (multi-line) |
| `list_ordered` | `true` | numbered list items (Markdown `1.`, Docling `enumerated`) | numbered item |
| `list_marker` | e.g. `3.`, `a)` | the marker the source used, when known | that marker; without one, items are numbered per run |
| `code_language` | e.g. `go` | Markdown ```` ```lang ```` fences | fenced block with that info string |

`header` / `footer` elements are furniture too: the Markdown and text views skip them, JSON
keeps them.

### SourceLocation

```jsonc
{ "kind": "pdf" | "sheet" | "slide" | "none",
  "page": 1,
  "bbox": { "coord": "pdf_user_space_v1" | "pdf_top_left_v1" | "image_px_v1",
            "x0": …, "y0": …, "x1": …, "y1": … },
  "sheet": "Summary",
  "cell_range": "A1:D9",
  "line": 42 }
```

**Coordinate systems are named, not implied.** Docling emits `BOTTOMLEFT` for PDFs and
`TOPLEFT` for XLSX; the mapper records which one it received so consumers never mix
origins. If an engine gives no geometry, `bbox` is absent — never zero-filled.

### Table / Cell

```jsonc
{ "id": "tbl_1", "rows": 5, "cols": 4, "header_rows": 1,
  "source_location": { "kind": "pdf", "page": 1,
                       "bbox": { "coord": "pdf_user_space_v1", … } },
  "caption": "…", "confidence": 0.88,
  "cells": [ { "row": 0, "col": 0, "row_span": 1, "col_span": 1,
               "text": "Region", "formula": "=SUM(B2:B5)", "cached_value": "128400",
               "data_type": "string|number|date|bool", "confidence": … } ] }
```

Merged cells appear **once, at their anchor** with `row_span`/`col_span`. A pipe table cannot
express a span (or more than one header row), so the Markdown view renders a table that has
any `row_span`/`col_span > 1` or `header_rows > 1` as an HTML `<table>` with `rowspan`/`colspan` (cell text HTML-escaped, the first
`header_rows` rows as `<th>`); every other table is a GFM pipe table whose cell line breaks
become `<br>` and whose `|` is escaped. `formula`/`cached_value` are only present when the
engine exposes them (XLSX via structured engines).

### Warning

```jsonc
{ "code": "ocr_low_confidence", "message": "…", "severity": "info|warn|error",
  "source_location": { … } }
```

OCR-related codes are three distinct things and must not be conflated:

| Code | Meaning | Usable as a quality signal? |
| --- | --- | --- |
| `ocr_used` | OCR ran on at least one page. | **No.** It is the normal path for a scan. |
| `ocr_quality_unknown` | OCR ran but the backend reported no confidence (e.g. PP-OCRv6). | **No.** Unknown is not low. |
| `ocr_low_confidence` | A page reported a confidence below 0.60; the message quotes the score and page. | Yes, for scheduling only. |
| `no_text_layer` | Extraction produced fewer than 20 characters on a PDF/image. | Yes — it means extraction failed, not that the input was scanned. |

Stable codes: `ocr_not_run`, `ocr_used`, `ocr_quality_unknown`, `ocr_low_confidence`, `no_text_layer`, `truncated`,
`page_limit_reached`, `partial_content`, `encrypted`, `degraded_conversion`,
`unsupported_element`, `layout_heuristic`, `no_coordinates`, plus adapter-specific
`sheet_name_unavailable`, `encoding_converted`, `no_page_boundaries`,
`formula_text_unavailable`, `ocr_quality_unknown`, `legacy_format`.

`legacy_format` marks OLE2 Word/Excel/PowerPoint input: text and tables are
extracted, page numbers and coordinates are not available.

## 2a. Compatibility policy (what `doc-ir/0.1` promises)

The IR is a public contract, so the rules for changing it are part of it:

* **Additive only within a minor version.** New optional fields may appear in any
  object; clients must ignore fields they do not know. A field is never removed
  or given a different meaning inside `doc-ir/0.1`.
* **Vocabulary is open, not enumerated, on the client side.** `elements[].type`
  and `warnings[].code` are strings. The server only emits the documented values,
  but a client must tolerate an unknown one (render it as `unknown`, keep the
  text) instead of failing the document. Generated clients are built with
  `enumUnknownDefaultCase` for this reason.
* **`schema_version` is a contract selector, not a content fingerprint.** It
  changes only for a breaking change (a removed field, a renamed field, a changed
  meaning). A client that sees an unknown `schema_version` should refuse the
  document rather than guess.
* **`routing.actual_device`** is `gpu`, `cpu` or `unknown`. A new value would be
  an additive vocabulary change; treat anything unrecognised as `unknown`.
* **Numbers and units are fixed.** `metrics.parse_ms` is milliseconds,
  `size_bytes` is bytes, `bbox` coordinates are in the space named by
  `bbox.coord`; absent fields mean "not measured", never zero.
* **Arrays are in reading order.** `elements[]` order is authoritative;
  `reading_order` is a per-element copy for consumers that sort or filter, and the
  two must agree.

`internal/documentir/compat_test.go` enforces the client-side half of this: a
document with an unknown element type, an unknown warning code and unknown fields
must unmarshal and round-trip unchanged.

## 3. Rules the adapters must follow

1. **Never invent.** Missing page number, bbox, sheet name, confidence or heading level is
   absent. No placeholder coordinates, no guessed confidence, no synthesised sheet names.
2. **Reading order is the element array order.** Engines that provide a body tree
   (Docling) are walked depth-first; engines that provide page text are split per page,
   preserving page order.
3. **Engine identity is recorded.** `parser_engine` + `parser_version` + `routing.reason`
   make every result auditable ("which engine produced this and why").
4. **Degradation is a warning, not silence.** e.g. docling-rs `--text-layer-only` adds
   `layout_heuristic`; Tika adds `no_coordinates`; xberg XLSX adds `sheet_name_unavailable`.
5. **Confidence is engine-provided only.** Currently only xberg reports per-document
   quality; no adapter fabricates per-element confidence.
6. **Nothing binary in JSON.** Images are referenced as artifacts.

## 4. Engine → IR mapping (implemented and tested)

| Engine | pages | bbox | reading order | headings | tables | sheet names | confidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| docling-rs / docling-serve | ✅ `pages` map | ✅ + origin | ✅ body tree, every child kind (a table under its section header stays there); unreached nodes inserted by texts-array position | ✅ `section_header` + level; sheets as `##` headings | ✅ cells + spans | ✅ `groups[label=sheet].name` | ❌ |
| xberg | ✅ page split | ✅ tables only | page Markdown order (`--content-format markdown`); tables at their inline position, enriched with the structured table's bbox | ✅ Markdown headings; lists, quotes and inline formatting (`rich_text`) | ✅ grid | ❌ (warning; sheets become `##` headings from `sheet_name`) | ⚠️ document-level quality score |
| MinerU (flash) | ✅ `<!-- page N of M -->` | ❌ | markdown order | ⚠️ from markdown | ✅ via markdown tables | ❌ | ❌ |
| Tika | ⚠️ only when form feeds exist (never for PDF in 4.2) | ❌ | text order | ⚠️ markdown headings for OOXML | ⚠️ markdown tables for OOXML | ❌ | ❌ |
| legacy `.doc/.xls/.ppt` | ❌ (`legacy_format` warning) | ❌ | text/table order | xberg: no; docling: section headings where present | xberg: grid per sheet; docling: table cells | xlsx only via `.xls` sheet index | ❌ |
| native | – (line) | ❌ | line order | ✅ markdown headings | ✅ csv/tsv grid | ⚠️ `csv` pseudo-sheet | ❌ |

Docling-specific mapping details: an `inline` group (a paragraph made of formatted runs)
becomes **one** element whose `rich_text` carries each run's `formatting` (bold, italic,
strikethrough) and `hyperlink`; `enumerated`/`marker` become `list_ordered`/`list_marker`;
list nesting comes from nested list groups; `code_language` is kept. docling-rs 2.53.3
reports run formatting and hyperlinks for DOCX only; its PDF text carries none, and a
formatted run inside a heading or a table cell is flattened by the engine itself.
`content_layer=furniture` (e.g. the DOCX "page header"/"page footer" groups) is kept in JSON
with `metadata.content_layer=furniture`, typed `header`/`footer` where the group says so.

## 5. Validation performed in Phase 0

| Requirement (§7) | Result |
| --- | --- |
| Excel multi-sheet | docling-rs: `Summary` + `Detail` preserved with sheet names; xberg: 2 sheets, names unavailable (warning) |
| Excel formulas | docling-rs returns computed cell text; formula text is not exposed by either engine → `formula` stays empty (documented gap) |
| Merged cells | docling-rs `row_span/col_span` present; xberg expands to grid (checked on the merged-note fixture) |
| Numeric precision | `128400.125` preserved through xberg/docling-rs |
| CSV encoding | native engine: UTF-8, UTF-8-BOM, UTF-16, **GB18030** OK; xberg: GB18030 mojibake (recorded, not routed) |
| CSV delimiter | `,` `;` `\t` `|` sniffed by native engine |
| PDF reading order | docling-rs body order preserved (two-column fixture ordered left column then right) |
| PDF page numbers | present for all page-based engines |
| Heading hierarchy | docling-rs levels preserved; markdown-derived levels only where the engine emitted them |
| OCR locality/confidence | **gap**: no candidate returns per-word/line bbox + confidence for scans in the tested configuration; results carry `ocr_used`/`ocr_quality_unknown`/`no_coordinates` warnings instead, and `ocr_low_confidence` only when a backend actually reported a score below threshold |
| Page box (for rendering a bbox) | `source_location.page_width_pt` / `page_height_pt` are populated from Docling's page map **for PDF/slide only** (points, 1/72"). For XLSX, Docling's "page size" is a cell-grid extent, so it is deliberately not reported as points; use `sheet` + `cell_range`. Absent elsewhere (PR-00 / F08) |
| Tika page numbers | only when Tika emits page boundaries (form feeds). Tika 4.2 does **not** for PDFs, so PR-00 changed the adapter to omit `page` and warn `no_page_boundaries` instead of stamping page 1 on the whole document |

## 6. Known gaps (deliberate, tracked)

* **Formula text** is not exposed by any engine, so ParseDock reads it from the
  worksheet XML (`internal/engines/ooxml.go`): `<c><f>…</f><v>cached</v></c>` becomes
  `cell.formula` / `cell.cached_value`. Only cells the engine already produced are
  enriched; shared-formula slaves (which carry no text of their own) keep their
  cached value and produce a `formula_text_unavailable` warning rather than a
  guessed formula.
* **OCR confidence/bbox**: tesseract's word boxes are not surfaced by xberg's JSON output;
  docling-rs reports page/region boxes but not per-word confidence.
* **MinerU flash bbox**: page granularity only.
* **Image extraction**: `image_ref` is defined but **no adapter emits it and no
  artifact download endpoint exists**. It is a reserved field: a test pins that no
  adapter invents one, and `Document.HasImageRefs()` exists so a future adapter
  cannot silently introduce a dangling reference. Activating it requires an
  authorized artifact endpoint plus ACK/TTL participation (PR-05).
* **Formula text**: `Cell.Formula` is never populated — neither xberg nor docling-rs
  exposes the OOXML `<f>` element. `cached_value`/text is what the engines give.
  Reading formulas from the worksheet XML is a PR-05 task and must cover shared
  formulas, dates and precision; until then a fixture test asserts the field stays
  empty rather than fabricated.
* **Page rotation**: `rotation_deg` is defined but not populated (no engine reports
  it for the tested corpus).
* **Reading order for layout-sensitive PDFs** is engine-dependent. xberg orders the
  columns of a two-column page left then right (17/17 two-column PDFs in
  `benchmark/fixtures/`), but where text runs are drawn on top of each other, as in
  `pdf_two_column_en.pdf`, it merges the overlapping lines word by word; docling-rs keeps
  them apart. Tika emits plain content-stream order. `mode=auto` routes PDFs to xberg
  and does **not** escalate on layout (no signal tested was reliable enough, see
  `docs/DEPLOYMENT.md` §4); use `mode=accurate` for such documents.

Each gap is either a `warning` in the output or listed here; none of them is silently
"fixed" by generating data.
