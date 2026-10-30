# DOCUMENT IR v0.1

Schema id: `doc-ir/0.1` (const `documentir.SchemaVersion`).
Implemented in `internal/documentir/ir.go`; renderers in `export.go`.
JSON is the IR itself; Markdown and Text are **derived views**, never the source of truth.

## 1. Why not "just Markdown"

* Markdown loses page numbers, coordinates, sheet names, cell spans, reading order and confidence.
* Different engines emit different Markdown dialects; storing that would freeze one engine's quirks
  into the contract.
* Downstream (Chat citations, RAG metadata, audit trails) needs `page`/`bbox`/`sheet` provenance.

Therefore ParseDock stores a structured IR and renders three views:

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
                "engine", "fallback", "reason" },
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

### Element

```jsonc
{
  "id": "el_2",
  "type": "title|section|paragraph|list|list_item|table|image|formula|caption|footnote|header|footer|code|page_break|unknown",
  "text": "…",
  "level": 2,                        // heading level or list nesting; 0/absent otherwise
  "reading_order": 1,
  "confidence": 0.93,                // only when the engine actually reports it
  "source_location": { … },
  "table_id": "tbl_1",               // when type=table
  "image_ref": "",                    // RESERVED: no adapter emits one yet (see §6)
  "metadata": { "content_layer": "furniture" }
}
```

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

Merged cells appear **once, at their anchor** with `row_span`/`col_span`; the renderer expands
them into a rectangular grid for Markdown. `formula`/`cached_value` are only present when the
engine exposes them (XLSX via structured engines).

### Warning

```jsonc
{ "code": "ocr_low_confidence", "message": "…", "severity": "info|warn|error",
  "source_location": { … } }
```

Stable codes: `ocr_not_run`, `ocr_low_confidence`, `no_text_layer`, `truncated`,
`page_limit_reached`, `partial_content`, `encrypted`, `degraded_conversion`,
`unsupported_element`, `layout_heuristic`, `no_coordinates`, plus adapter-specific
`sheet_name_unavailable`, `encoding_converted`, `no_page_boundaries`,
`formula_text_unavailable`, `ocr_quality_unknown`.

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
| docling-rs / docling-serve | ✅ `pages` map | ✅ + origin | ✅ body tree | ✅ `section_header` + level | ✅ cells + spans | ✅ `groups[label=sheet].name` | ❌ |
| xberg | ✅ page split | ✅ tables only | page text order + tables | ⚠️ from `style_name` annotations | ✅ grid | ❌ (warning) | ⚠️ document-level quality score |
| MinerU (flash) | ✅ `<!-- page N of M -->` | ❌ | markdown order | ⚠️ from markdown | ✅ via markdown tables | ❌ | ❌ |
| Tika | ⚠️ only when form feeds exist (never for PDF in 4.2) | ❌ | text order | ⚠️ markdown headings for OOXML | ⚠️ markdown tables for OOXML | ❌ | ❌ |
| native | – (line) | ❌ | line order | ✅ markdown headings | ✅ csv/tsv grid | ⚠️ `csv` pseudo-sheet | ❌ |

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
| OCR locality/confidence | **gap**: no candidate returns per-word/line bbox + confidence for scans in the tested configuration; results carry `ocr_low_confidence`/`no_coordinates` warnings instead |
| Page box (for rendering a bbox) | `source_location.page_width_pt` / `page_height_pt` are populated from Docling's page map **for PDF/slide only** (points, 1/72"). For XLSX, Docling's "page size" is a cell-grid extent, so it is deliberately not reported as points; use `sheet` + `cell_range`. Absent elsewhere (PR-00 / F08) |
| Tika page numbers | only when Tika emits page boundaries (form feeds). Tika 4.2 does **not** for PDFs, so PR-00 changed the adapter to omit `page` and warn `no_page_boundaries` instead of stamping page 1 on the whole document |

## 6. Known gaps (deliberate, tracked)

* **Formula text** (as opposed to cached value) is not extracted by the tested engines.
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
* **Reading order for two-column PDFs** is engine-dependent; docling-rs handles it,
  Tika and xberg do not (plain text order).

Each gap is either a `warning` in the output or listed here; none of them is silently
"fixed" by generating data.
