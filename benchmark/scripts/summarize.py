#!/usr/bin/env python3
"""Build summary tables from benchmark/results/raw/*.jsonl.

Kept separate from bench.py so that summary tables can be rebuilt (and merged
across runs) without re-running any engine.

    python benchmark/scripts/summarize.py
"""
from __future__ import annotations

import csv
import json
import statistics
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RAW = ROOT / "benchmark" / "results" / "raw"

# Formats each engine actually supports (mirrors internal/engines/capabilities.go).
# Rows recorded by older runs before the harness learned to skip unsupported
# formats are filtered out here so the tables stay honest.
SUPPORTED = {
    "native": {"txt", "md", "csv", "tsv"},
    "xberg": {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp"},
    "docling-rs": {"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"},
    "docling-rs-textlayer": {"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"},
    "docling-serve": {"pdf", "docx", "xlsx", "pptx", "html", "md", "png", "jpeg", "webp"},
    "mineru": {"pdf", "png", "jpeg", "webp"},
    "tika": {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"},
}


def supported(r: dict) -> bool:
    fmts = SUPPORTED.get(r.get("engine", ""))
    return fmts is None or r.get("format") in fmts
OUT = ROOT / "benchmark" / "results" / "summary"


def load() -> list[dict]:
    rows = []
    for f in sorted(RAW.glob("bench-*.jsonl")):
        for line in f.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                r = json.loads(line)
            except json.JSONDecodeError:
                continue
            r["_source"] = f.name
            rows.append(r)
    return rows


def latest(rows: list[dict], keyfn) -> dict:
    out: dict = {}
    for r in rows:  # files are sorted, later records win
        k = keyfn(r)
        out[k] = r
    return out


def main() -> int:
    rows = load()
    OUT.mkdir(parents=True, exist_ok=True)

    # ---- quality: one row per (engine, file), newest ok record -------------
    q = [r for r in rows if r.get("phase") == "quality" and r.get("status") == "ok" and supported(r)]
    best = latest(q, lambda r: (r["engine"], r["file"]))
    with open(OUT / "benchmarks-quality.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["engine", "version", "config_hash", "file", "file_sha256", "format", "category",
                    "pages", "status", "elapsed_ms", "chars", "cer", "wer", "token_f1",
                    "table_cell_accuracy", "table_header_accuracy", "has_bbox", "table_count",
                    "output_hash"])
        for (eng, file), r in sorted(best.items()):
            st = r.get("structure") or {}
            ta = r.get("table_accuracy") or {}
            w.writerow([eng, r.get("version"), r.get("config_hash"), file, r.get("file_sha256"),
                        r.get("format"), r.get("category"), r.get("pages"), r.get("status"),
                        r.get("elapsed_ms"), r.get("chars"), r.get("cer", ""), r.get("wer", ""),
                        r.get("token_f1", ""), ta.get("cell_accuracy", ""),
                        ta.get("header_accuracy", ""), st.get("has_bbox", ""),
                        st.get("table_count", ""), r.get("output_hash")])

    # ---- perf: warm medians per (engine, file) over the newest run group --
    p = [r for r in rows if r.get("phase") == "perf" and r.get("kind") == "warm"
         and r.get("status") == "ok" and supported(r)]
    groups: dict = {}
    for r in p:
        groups.setdefault((r["engine"], r["file"]), []).append(r)
    with open(OUT / "benchmarks-perf.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["engine", "version", "config_hash", "file", "file_sha256", "format", "pages",
                    "mode", "device", "runs", "median_ms", "p95_ms", "median_rss_mb",
                    "peak_rss_mb", "cold_ms", "status"])
        for (eng, file), rs in sorted(groups.items()):
            rs = rs[-5:]  # the newest 5 warm runs
            ms = sorted(r["elapsed_ms"] for r in rs)
            rss = [r["peak_rss_mb"] for r in rs if r.get("peak_rss_mb")]
            cold = [r["elapsed_ms"] for r in p if r["engine"] == eng and r["file"] == file]
            w.writerow([eng, rs[0].get("version"), rs[0].get("config_hash"), file,
                        rs[0].get("file_sha256"), rs[0].get("format"), rs[0].get("pages"),
                        rs[0].get("mode"), rs[0].get("device"), len(rs),
                        round(statistics.median(ms), 1),
                        round(ms[max(0, int(len(ms) * 0.95) - 1)], 1),
                        round(statistics.median(rss), 1) if rss else "",
                        round(max(rss), 1) if rss else "",
                        round(statistics.median(cold), 1) if cold else "", "ok"])

    # ---- concurrency: newest record per (engine, file, concurrency) --------
    c = [r for r in rows if r.get("phase") == "concurrency" and supported(r)]
    cbest = latest(c, lambda r: (r["engine"], r["file"], r["concurrency"]))
    with open(OUT / "benchmarks-concurrency.csv", "w", newline="") as f:
        w = csv.writer(f)
        w.writerow(["engine", "file", "format", "concurrency", "docs", "errors", "wall_s",
                    "docs_per_s", "pages_per_s", "p50_ms", "max_ms", "status"])
        for (eng, file, level), r in sorted(cbest.items()):
            w.writerow([eng, file, r.get("format"), level, r.get("docs"), r.get("errors"),
                        r.get("wall_s"), r.get("docs_per_s"), r.get("pages_per_s"),
                        r.get("p50_ms"), r.get("max_ms"), r.get("status")])

    print(f"quality rows:     {len(best)}")
    print(f"perf groups:      {len(groups)}")
    print(f"concurrency rows: {len(cbest)}")
    for name in ("benchmarks-quality.csv", "benchmarks-perf.csv", "benchmarks-concurrency.csv"):
        p = OUT / name
        print(f"  {name}: {p.stat().st_size} bytes")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
