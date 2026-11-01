<!-- Generated from ParseDock docs/DOCUMENT-IR.md (API 0.1.0) by scripts/sync_skills.py. Do not edit here. -->

# Document IR `doc-ir/0.1`

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
text budget as `text`.

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
