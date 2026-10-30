#!/usr/bin/env python3
"""Single source of truth for engine scoring (PR-00 / F01).

Reads the benchmark summary CSVs and computes every score quoted in
`docs/ENGINE-BENCHMARK.md` and `docs/ENGINE-DECISION.md`, so the two documents
cannot drift from the data again.

Rules (all covered by tests/unit/test_score.py):

* **Two explicit sample domains.** PDF/Office engines are scored on the
  `pdf_office` set; the text engine on the `text` set. Comparing a CSV engine to
  PDF gold text was the original bug.
* **Applicability, not zeros.** A sample whose format an engine cannot read is
  `n/a` and excluded, and the count is reported. Unsupported formats never turn
  into a silent 0 — except in the *coverage* dimension, where they are the point.
* **Structure is scored on samples that have a table.** A missing table counts
  as 0 (otherwise an engine that emits nothing would look perfect).
* **Provenance is scored per sample against the signals that sample can carry**
  (page, bbox, sheet). This is why text formats carry no provenance.
* **Scans are double weighted**; CER is clamped to [0, 1] (CER > 1 means
  insertions, e.g. Tika duplicating a page; clamp keeps it from dominating).
* **Unmeasured dimensions stay `None`** and the weighted total is computed over
  the measured ones with renormalised weights, flagged `incomplete`. Filling an
  unmeasured RSS with 0 (the first draft) silently penalised two engines.
* Weights and sub-weights sum to 1.0 — asserted at import time.

Usage:
    python benchmark/scripts/score.py             # markdown tables (docs)
    python benchmark/scripts/score.py --json      # machine readable
    python benchmark/scripts/score.py --write     # also writes summary/scores.json
"""
from __future__ import annotations

import argparse
import csv
import json
import sys
from dataclasses import dataclass, field
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SUMMARY = ROOT / "benchmark" / "results" / "summary"

#: Five dimensions from the Phase 0 task guide.
WEIGHTS = {"quality": 0.40, "performance": 0.25, "resources": 0.15,
           "coverage": 0.10, "ops_license": 0.10}

#: Quality is deliberately three different things (F02).
QUALITY_SUBWEIGHTS = {"text": 0.60, "structure": 0.25, "provenance": 0.15}

#: Samples. `expect` lists the provenance signals the sample can carry;
#: `table` says whether the fixture contains a table to score.
PDF_OFFICE_SAMPLES = [
    {"file": "generated/pdf_text_en_3p.pdf", "format": "pdf", "label": "en PDF 3p",
     "expect": ["page", "bbox"], "table": True},
    {"file": "generated/pdf_text_zh_2p.pdf", "format": "pdf", "label": "zh PDF 2p",
     "expect": ["page", "bbox"], "table": True},
    {"file": "generated/pdf_two_column_en.pdf", "format": "pdf", "label": "two-column",
     "expect": ["page", "bbox"], "table": False},
    {"file": "generated/pdf_scanned_en.pdf", "format": "pdf", "label": "scan EN",
     "expect": ["page", "bbox"], "table": False, "double_weight": True},
    {"file": "generated/pdf_scanned_zh.pdf", "format": "pdf", "label": "scan ZH",
     "expect": ["page", "bbox"], "table": False, "double_weight": True},
    {"file": "generated/docx_report.docx", "format": "docx", "label": "docx",
     "expect": ["bbox"], "table": True},
    {"file": "generated/xlsx_workbook.xlsx", "format": "xlsx", "label": "xlsx",
     "expect": ["sheet", "bbox"], "table": True},
]
TEXT_SAMPLES = [
    {"file": "generated/csv_utf8.csv", "format": "csv", "label": "csv UTF-8",
     "expect": ["sheet"], "table": True},
    {"file": "generated/csv_gb18030.csv", "format": "csv", "label": "csv GB18030",
     "expect": ["sheet"], "table": True},
    {"file": "generated/tsv_data.tsv", "format": "tsv", "label": "tsv",
     "expect": ["sheet"], "table": True},
    {"file": "generated/txt_plain.txt", "format": "txt", "label": "txt", "expect": [], "table": False},
    {"file": "generated/md_notes.md", "format": "md", "label": "md", "expect": [], "table": False},
]
DOMAINS = {"pdf_office": PDF_OFFICE_SAMPLES, "text": TEXT_SAMPLES}
ENGINE_DOMAIN = {"native": "text"}

#: Which formats an engine can actually read (mirrors internal/engines/capabilities.go).
SUPPORTED = {
    "native": {"txt", "md", "csv", "tsv"},
    "xberg": {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp"},
    "docling-rs": {"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"},
    "docling-rs-textlayer": {"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"},
    "docling-serve": {"pdf", "docx", "xlsx", "pptx", "html", "md", "png", "jpeg", "webp"},
    "mineru": {"pdf", "png", "jpeg", "webp"},
    "tika": {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"},
}

TARGET_FORMATS = ["pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp"]

MIN_SAMPLES = 3

#: Performance buckets from warm p95 of the 3-page PDF and the 1-page scan.
PERF_BUCKETS = [
    (30, 400, 10.0), (50, 800, 9.5), (100, 1000, 9.0), (250, 1500, 8.0),
    (500, 2000, 7.0), (1000, 2500, 5.0), (2000, 3500, 3.0), (4000, 6000, 2.0),
]
#: Text-domain performance: p95 of the csv sample (ms).
TEXT_PERF_BUCKETS = [(2, 10.0), (10, 9.0), (50, 7.0), (200, 5.0), (1000, 3.0)]

RSS_BUCKETS = [(100, 10.0), (300, 8.0), (800, 6.0), (1500, 4.0)]
RSS_FLOOR = 2.0
RESIDENT_PENALTY = {"mineru", "docling-serve"}

#: Ops/licence is a judgement; recorded with reasons instead of invented per run.
OPS_RUBRIC = {
    "native": (10.0, ["in-process Go, no external artifact"]),
    "docling-rs": (9.0, ["single binary + pinned model bundle", "MIT code and models",
                         "no moving tags"]),
    # bench-only variant of docling-rs (same binary, --text-layer-only)
    "docling-rs-textlayer": (9.0, ["same artifact as docling-rs, text-layer-only mode",
                                   "loses tables/reading order by design"]),
    "docling-serve": (8.0, ["MIT", "7.2 GB Python runtime", "extra service to operate"]),
    "tika": (7.0, ["Apache-2.0", "JVM service", "latest-full is a moving tag (pinned by digest manually)"]),
    "xberg": (6.0, ["MIT core", "release tarball bundles libx265 (GPL-2.0) — blocked on L1",
                    "requires glibc >= 2.38"]),
    "mineru": (5.0, ["Apache-2.0 + additional commercial/attribution terms",
                     "local daemon + document library", "telemetry on by default",
                     "AGPL-3.0 VLM weights used by the remote tiers"]),
}

assert abs(sum(WEIGHTS.values()) - 1.0) < 1e-9, "dimension weights must sum to 1.0"
assert abs(sum(QUALITY_SUBWEIGHTS.values()) - 1.0) < 1e-9, "quality sub-weights must sum to 1.0"


# ---------------------------------------------------------------- helpers

def clamp_cer(value: float) -> float:
    return max(0.0, min(1.0, value))


def load_csv(name: str) -> list[dict]:
    path = SUMMARY / name
    if not path.exists():
        return []
    with open(path, newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def fnum(row: dict | None, key: str) -> float | None:
    if not row:
        return None
    v = row.get(key)
    if v is None or v == "":
        return None
    try:
        return float(v)
    except ValueError:
        return None


def truthy(row: dict | None, key: str) -> bool:
    return bool(row) and str(row.get(key, "")).lower() == "true"


def supports(engine: str, fmt: str) -> bool:
    return fmt in SUPPORTED.get(engine, set())


def samples_for(engine: str) -> list[dict]:
    return DOMAINS[ENGINE_DOMAIN.get(engine, "pdf_office")]


# ---------------------------------------------------------------- scoring

@dataclass
class EngineScore:
    engine: str
    version: str = ""
    domain: str = "pdf_office"
    text: float | None = None
    structure: float | None = None
    provenance: float | None = None
    quality: float | None = None
    performance: float | None = None
    resources: float | None = None
    coverage: float | None = None
    ops_license: float | None = None
    weighted: float | None = None
    measured_dimensions: list = field(default_factory=list)
    samples_used: list = field(default_factory=list)
    samples_missing: list = field(default_factory=list)
    low_confidence: bool = False
    incomplete: bool = False
    notes: list = field(default_factory=list)

    def as_dict(self) -> dict:
        return {
            "engine": self.engine, "version": self.version, "domain": self.domain,
            "quality_sub": {"text": self.text, "structure": self.structure,
                            "provenance": self.provenance},
            "dimensions": {"quality": self.quality, "performance": self.performance,
                           "resources": self.resources, "coverage": self.coverage,
                           "ops_license": self.ops_license},
            "weighted": self.weighted,
            "measured_dimensions": self.measured_dimensions,
            "samples_used": self.samples_used,
            "samples_missing": self.samples_missing,
            "low_confidence": self.low_confidence,
            "incomplete": self.incomplete,
            "notes": self.notes,
        }


def quality_scores(engine: str, qrows: dict[str, dict]) -> dict:
    samples = samples_for(engine)
    cer_num = cer_den = 0.0
    used, missing, notes = [], [], []
    table_scores, prov_scores = [], []

    for s in samples:
        if not supports(engine, s["format"]):
            missing.append({"file": s["file"], "reason": "format not supported"})
            continue
        row = qrows.get(s["file"])
        cer = fnum(row, "cer")
        if row is None or cer is None:
            missing.append({"file": s["file"], "reason": "no measurement"})
            continue
        used.append(s["file"])
        w = 2.0 if s.get("double_weight") else 1.0
        cer_num += clamp_cer(cer) * w
        cer_den += w

        if s["table"]:
            cell = fnum(row, "table_cell_accuracy")
            hdr = fnum(row, "table_header_accuracy")
            table_scores.append(0.0 if cell is None else 0.7 * cell + 0.3 * (hdr or 0.0))

        if s["expect"]:
            present = sum(1 for sig in s["expect"] if truthy(row, f"has_{sig}"))
            prov_scores.append(present / len(s["expect"]))

    text = 10.0 * (1.0 - cer_num / cer_den) if cer_den else None
    structure = 10.0 * (sum(table_scores) / len(table_scores)) if table_scores else None
    provenance = 10.0 * (sum(prov_scores) / len(prov_scores)) if prov_scores else None

    if structure is not None and structure == 0.0:
        notes.append("no table structure recovered on any table sample")
    if provenance is None:
        notes.append("no provenance signals expected for this domain")

    sub = {"text": text, "structure": structure, "provenance": provenance}
    present_sub = {k: v for k, v in sub.items() if v is not None}
    if present_sub:
        wsum = sum(QUALITY_SUBWEIGHTS[k] for k in present_sub)
        quality = sum(QUALITY_SUBWEIGHTS[k] * v for k, v in present_sub.items()) / wsum
    else:
        quality = None
    return {"text": text, "structure": structure, "provenance": provenance,
            "quality": quality, "used": used, "missing": missing, "notes": notes}


def performance_score(engine: str, prows: dict[str, dict]) -> tuple[float | None, list]:
    notes = []
    if ENGINE_DOMAIN.get(engine) == "text":
        row = prows.get("generated/csv_utf8.csv") or prows.get("generated/txt_plain.txt")
        p95 = fnum(row, "p95_ms")
        if p95 is None:
            return None, ["no text-domain latency sample"]
        for limit, score in TEXT_PERF_BUCKETS:
            if p95 <= limit:
                return score, [f"p95 text sample {p95:.1f} ms"]
        return 1.0, [f"p95 text sample {p95:.1f} ms (slower than the worst bucket)"]

    pdf = prows.get("generated/pdf_text_en_3p.pdf")
    scan = prows.get("generated/pdf_scanned_en.pdf")
    p95_pdf, p95_scan = fnum(pdf, "p95_ms"), fnum(scan, "p95_ms")
    if p95_pdf is None or p95_scan is None:
        return None, ["missing reference latency samples"]
    notes.append(f"p95 3-page PDF {p95_pdf:.0f} ms, scan {p95_scan:.0f} ms")
    for max_pdf, max_scan, score in PERF_BUCKETS:
        if p95_pdf <= max_pdf and p95_scan <= max_scan:
            return score, notes
    return 1.0, notes + ["slower than the worst bucket"]


def resource_score(engine: str, prows: dict[str, dict]) -> tuple[float | None, list]:
    rss_values = [fnum(r, "median_rss_mb") for r in prows.values()]
    rss_values = [v for v in rss_values if v]
    if not rss_values:
        return None, ["not measured: no per-request RSS for this server/client"]
    rss = max(rss_values)
    score = RSS_FLOOR
    for limit, s in RSS_BUCKETS:
        if rss <= limit:
            score = s
            break
    notes = [f"max median RSS across samples {rss:.0f} MiB"]
    if engine in RESIDENT_PENALTY:
        score = max(1.0, score - 1.0)
        notes.append("resident daemon/VM: -1")
    return score, notes


def coverage_score(engine: str) -> float:
    fmts = SUPPORTED.get(engine, set())
    return round(10.0 * sum(1 for f in TARGET_FORMATS if f in fmts) / len(TARGET_FORMATS), 2)


def weighted_total(dims: dict[str, float | None]) -> tuple[float | None, list, bool]:
    measured = {k: v for k, v in dims.items() if v is not None}
    if not measured:
        return None, [], True
    total_w = sum(WEIGHTS[k] for k in measured)
    total = sum(WEIGHTS[k] * v for k, v in measured.items()) / total_w
    return round(total, 2), sorted(measured), len(measured) < len(WEIGHTS)


def score_all() -> dict:
    quality_by_engine: dict[str, dict[str, dict]] = {}
    for row in load_csv("benchmarks-quality.csv"):
        quality_by_engine.setdefault(row["engine"], {})[row["file"]] = row
    perf_by_engine: dict[str, dict[str, dict]] = {}
    for row in load_csv("benchmarks-perf.csv"):
        perf_by_engine.setdefault(row["engine"], {})[row["file"]] = row

    out = []
    for engine in sorted(quality_by_engine):
        qrows = quality_by_engine[engine]
        prows = perf_by_engine.get(engine, {})
        s = EngineScore(engine=engine, domain=ENGINE_DOMAIN.get(engine, "pdf_office"))
        s.version = next(iter(qrows.values())).get("version", "")

        q = quality_scores(engine, qrows)
        s.text, s.structure, s.provenance, s.quality = (q["text"], q["structure"],
                                                        q["provenance"], q["quality"])
        s.samples_used, s.samples_missing = q["used"], q["missing"]
        s.notes.extend(q["notes"])
        s.performance, pnotes = performance_score(engine, prows)
        s.notes.extend(pnotes)
        s.resources, rnotes = resource_score(engine, prows)
        s.notes.extend(rnotes)
        s.coverage = coverage_score(engine)
        ops = OPS_RUBRIC.get(engine)
        if ops:
            s.ops_license, reasons = ops
            s.notes.extend(f"ops: {r}" for r in reasons)
        else:
            s.notes.append("ops/licence not assessed")

        dims = {"quality": s.quality, "performance": s.performance, "resources": s.resources,
                "coverage": s.coverage, "ops_license": s.ops_license}
        s.weighted, s.measured_dimensions, s.incomplete = weighted_total(dims)
        s.low_confidence = len(s.samples_used) < MIN_SAMPLES
        out.append(s)

    return {
        "rubric": {
            "weights": WEIGHTS,
            "quality_subweights": QUALITY_SUBWEIGHTS,
            "domains": {k: [s["file"] for s in v] for k, v in DOMAINS.items()},
            "min_samples": MIN_SAMPLES,
            "target_formats": TARGET_FORMATS,
            "perf_buckets": {"pdf_office": PERF_BUCKETS, "text": TEXT_PERF_BUCKETS},
            "rss_buckets": RSS_BUCKETS,
        },
        "engines": [s.as_dict() for s in out],
        "source_files": ["benchmarks-quality.csv", "benchmarks-perf.csv"],
    }


# ---------------------------------------------------------------- rendering

def fmt(v: float | None, digits: int = 2) -> str:
    return "n/a" if v is None else f"{v:.{digits}f}"


def render_quality_table(result: dict) -> str:
    lines = ["| engine | domain | text fidelity | structure fidelity | provenance fidelity | **quality** | samples | flags |",
             "| --- | --- | --- | --- | --- | --- | --- | --- |"]
    for e in sorted(result["engines"], key=lambda x: -(x["weighted"] or 0)):
        q = e["quality_sub"]
        flags = []
        if e["low_confidence"]:
            flags.append("low confidence")
        if e["incomplete"]:
            flags.append("incomplete")
        n = len(result["rubric"]["domains"][e["domain"]])
        lines.append(f"| {e['engine']} | {e['domain']} | {fmt(q['text'])} | {fmt(q['structure'])} | "
                     f"{fmt(q['provenance'])} | **{fmt(e['dimensions']['quality'])}** | "
                     f"{len(e['samples_used'])}/{n} | {', '.join(flags) or '—'} |")
    return "\n".join(lines)


def render_final_table(result: dict) -> str:
    lines = ["| engine | quality (40%) | performance (25%) | resources (15%) | coverage (10%) | ops/licence (10%) | **weighted** | measured |",
             "| --- | --- | --- | --- | --- | --- | --- | --- |"]
    for e in sorted(result["engines"], key=lambda x: -(x["weighted"] or 0)):
        d = e["dimensions"]
        lines.append(f"| {e['engine']} | {fmt(d['quality'])} | {fmt(d['performance'])} | "
                     f"{fmt(d['resources'])} | {fmt(d['coverage'])} | {fmt(d['ops_license'])} | "
                     f"**{fmt(e['weighted'])}** | {len(e['measured_dimensions'])}/5 |")
    return "\n".join(lines)


def render_markdown(result: dict) -> str:
    return "\n\n".join([
        "#### Quality: three separate fidelities\n",
        render_quality_table(result),
        "\n#### Weighted totals (weights renormalised over measured dimensions)\n",
        render_final_table(result),
    ])


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--json", action="store_true")
    ap.add_argument("--write", action="store_true")
    args = ap.parse_args()

    result = score_all()
    if args.write:
        SUMMARY.mkdir(parents=True, exist_ok=True)
        (SUMMARY / "scores.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    if args.json:
        print(json.dumps(result, indent=2))
        return 0
    print(render_markdown(result))
    if not result["engines"]:
        print("\n(no data: run `make bench` first)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
