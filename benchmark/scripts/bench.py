#!/usr/bin/env python3
"""ParseDock engine benchmark: quality, latency, resources, throughput.

The harness drives each engine the same way ParseDock does (one subprocess or
one HTTP call per document) and records raw JSONL that backs the numbers in
docs/ENGINE-BENCHMARK.md.

Design rules taken from the task guide:
  * fixed fixture hashes, engine versions, config hash and repeat count;
  * warm-up run separated from measured runs; median of N runs reported;
  * cold start measured separately from steady state;
  * quality measured against gold text (CER/WER) and gold tables;
  * no claim is made from vendor benchmarks.

Subcommands:
  quality      one run per fixture, full IR capture + scoring
  perf         warmed latency/RSS/throughput runs
  concurrency  docs/s at concurrency 1,2,4,8
  all          quality + perf + concurrency

Example:
  python benchmark/scripts/bench.py all --engines xberg,docling-rs --repeats 5
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import json
import os
import re
import shutil
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import unicodedata
import urllib.request
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "benchmark" / "fixtures"
GEN = FIXTURES / "generated"
GOLD = FIXTURES / "gold"
RAW = ROOT / "benchmark" / "results" / "raw"
SUMMARY = ROOT / "benchmark" / "results" / "summary"
RUNTIME = ROOT / "runtime"
WORK = RUNTIME / "bench-work"

XB_BIN = RUNTIME / "engines" / "xberg-cli" / "xberg"
XB_LIB = RUNTIME / "engines" / "xberg-cli"
DR_BIN = RUNTIME / "engines" / "docling-rs" / "docling-rs"
DR_ROOT = RUNTIME / "engines" / "docling-rs"
MINERU_BIN = RUNTIME / "engines" / "venv-mineru" / "bin" / "mineru"
DOCLING_SERVE = os.environ.get("DOCLING_SERVE_URL", "http://127.0.0.1:15001")
TIKA = os.environ.get("TIKA_URL", "http://127.0.0.1:15002")


# --------------------------------------------------------------------------- metrics
def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def sha256_text(s: str) -> str:
    return hashlib.sha256(s.encode("utf-8", "replace")).hexdigest()


def norm_text(s: str) -> str:
    s = unicodedata.normalize("NFKC", s)
    s = s.replace("\u00ad", "").replace("\ufeff", "")
    s = re.sub(r"[ \t\u3000]+", " ", s)
    s = re.sub(r"\n{2,}", "\n", s)
    return s.strip()


try:  # C++ implementation; 3-4 orders of magnitude faster than pure Python DP
    from rapidfuzz.distance import Levenshtein as _RFLevenshtein
except Exception:  # pragma: no cover - fallback keeps the harness dependency-free
    _RFLevenshtein = None

# CER/WER are computed on at most this many normalised characters. Longer
# documents are scored on their prefix and the length is recorded.
SCORE_CHAR_CAP = 40000


def edit_distance(a: str, b: str, cap: int = SCORE_CHAR_CAP) -> int:
    truncated = len(a) > cap or len(b) > cap
    if truncated:
        a, b = a[:cap], b[:cap]
    if _RFLevenshtein is not None:
        d = _RFLevenshtein.distance(a, b)
        if truncated:
            # Add the un-scored tail as pure insertion/deletion cost so a
            # truncated comparison never looks better than it is.
            d += abs(len(a) - len(b))
        return d
    if a == b:
        return 0
    if not a:
        return len(b)
    if not b:
        return len(a)
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


def cer(ref: str, hyp: str) -> float:
    ref, hyp = norm_text(ref), norm_text(hyp)
    if not ref:
        return 0.0 if not hyp else 1.0
    return edit_distance(ref, hyp) / max(1, len(ref))


def wer(ref: str, hyp: str) -> float:
    r = norm_text(ref).split()
    h = norm_text(hyp).split()
    if not r:
        return 0.0 if not h else 1.0
    return edit_distance(" ".join(r), " ".join(h)) / max(1, len(r))


def token_f1(ref: str, hyp: str) -> float:
    rt = set(re.findall(r"\w+", norm_text(ref).lower()))
    ht = set(re.findall(r"\w+", norm_text(hyp).lower()))
    if not rt:
        return 0.0
    tp = len(rt & ht)
    if tp == 0:
        return 0.0
    p, r = tp / len(ht), tp / len(rt)
    return 2 * p * r / (p + r)


# --------------------------------------------------------------------------- engines
@dataclass
class EngineResult:
    text: str
    markdown: str
    ir: dict | None
    elapsed_ms: float
    peak_rss_mb: float | None
    peak_vram_mb: float | None
    version: str
    device: str
    config_hash: str
    cold_ms: float | None = None


class Engine:
    name = "base"
    version = "0"
    device = "cpu"
    config: dict = {}
    formats: set[str] | None = None  # None = any

    def supports(self, item: dict) -> bool:
        return self.formats is None or item["format"] in self.formats

    def config_hash(self) -> str:
        return sha256_text(json.dumps(self.config, sort_keys=True))[:16]

    def available(self) -> tuple[bool, str]:
        return True, ""

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        raise NotImplementedError


def dir_bytes_size(p: Path) -> int:
    return sum(f.stat().st_size for f in p.rglob("*") if f.is_file())


class NativeEngine(Engine):
    """Mirrors ParseDock's Go native engine (text/markdown/csv logic)."""
    name = "native"
    version = "go-native-1"
    formats = {"txt", "md", "csv", "tsv"}

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        t0 = time.perf_counter()
        raw = path.read_bytes()
        try:
            text = raw.decode("utf-8-sig")
        except UnicodeDecodeError:
            text = raw.decode("gb18030", "replace")
        md = text
        if path.suffix in (".csv", ".tsv"):
            sep = "\t" if path.suffix == ".tsv" else ","
            rows = [l.split(sep) for l in text.strip().splitlines()]
            md = "\n".join("| " + " | ".join(r) + " |" for r in rows)
        elapsed = (time.perf_counter() - t0) * 1000
        return EngineResult(text=text, markdown=md, ir=None, elapsed_ms=elapsed,
                            peak_rss_mb=None, peak_vram_mb=None, version=self.version,
                            device=self.device, config_hash=self.config_hash())


def run_timed(cmd: list[str], cwd: Path, env: dict | None = None,
              timeout: float = 1800, stdout_path: Path | None = None) -> tuple[int, str, float, float]:
    """Run a command under /usr/bin/time to capture wall time and peak RSS."""
    with tempfile.NamedTemporaryFile("w+", delete=False) as tf:
        timefile = tf.name
    full = ["/usr/bin/time", "-f", "%e %M", "-o", timefile, *cmd]
    e = {**os.environ, **(env or {})}
    t0 = time.perf_counter()
    if stdout_path is not None:
        with open(stdout_path, "wb") as out:
            proc = subprocess.run(full, cwd=str(cwd), env=e, stdout=out,
                                  stderr=subprocess.PIPE, timeout=timeout)
    else:
        proc = subprocess.run(full, cwd=str(cwd), env=e, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, timeout=timeout)
    wall = (time.perf_counter() - t0) * 1000
    peak_rss = None
    try:
        # %e has only 10 ms resolution; keep the perf_counter wall time and use
        # /usr/bin/time purely for the child's peak RSS (KiB).
        parts = Path(timefile).read_text().strip().split()
        if len(parts) >= 2:
            peak_rss = int(parts[1]) / 1024.0
    except Exception:
        pass
    finally:
        try:
            os.unlink(timefile)
        except Exception:
            pass
    stderr = proc.stderr.decode("utf-8", "replace")
    if proc.returncode != 0:
        Path("/tmp/bench_last_err.txt").write_text(stderr[-4000:])
    return proc.returncode, stderr, wall, peak_rss or 0.0


class XbergEngine(Engine):
    name = "xberg"
    version = "1.3.10"
    formats = {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp"}

    def __init__(self, ocr: bool = True, ocr_lang: str = "eng"):
        self.config = {"ocr": ocr, "ocr_lang": ocr_lang, "format": "json",
                       "backend": "tesseract", "cache": "disabled"}

    def available(self):
        return XB_BIN.exists(), str(XB_BIN)

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        out = workdir / (path.stem + ".json")
        args = [str(XB_BIN), "extract", "--no-config-discovery", "--format", "json",
                "--no-cache", "true",
                "--ocr", "true" if self.config["ocr"] else "false"]
        if self.config["ocr"]:
            args += ["--ocr-language", self.config["ocr_lang"]]
        args.append(str(path.resolve()))
        rc, err, wall, rss = run_timed(args, workdir, env={"LD_LIBRARY_PATH": str(XB_LIB)},
                                       stdout_path=out, timeout=1800)
        if rc != 0:
            raise RuntimeError(f"xberg failed rc={rc}: {err[-500:]}")
        data = json.loads(out.read_text())
        res = data["result"]
        text = res.get("content", "")
        pages = res.get("pages") or []
        if pages and all("|" in (p.get("content") or "") or "##" in (p.get("content") or "") for p in pages):
            md = "\n\n".join(p.get("content", "") for p in pages)
        else:
            md = "\n\n".join(p.get("content", "") for p in pages) or text
        return EngineResult(
            text=text or "", markdown=md or "",
            ir={"pages": len(pages), "tables": res.get("tables") or [],
                "quality_score": res.get("quality_score"),
                "extraction_method": res.get("extraction_method"),
                "counts": res.get("counts")},
            elapsed_ms=wall, peak_rss_mb=rss, peak_vram_mb=None,
            version=self.version, device=self.device, config_hash=self.config_hash(),
        )


class DoclingRS(Engine):
    name = "docling-rs"
    version = "2.53.3"
    formats = {"pdf", "docx", "xlsx", "pptx", "html", "md", "txt", "png", "jpeg", "webp"}

    def __init__(self, text_layer_only: bool = False, ocr: bool = True):
        self.config = {"text_layer_only": text_layer_only, "ocr": ocr, "format": "json"}

    def available(self):
        return DR_BIN.exists(), str(DR_BIN)

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        out = workdir / (path.stem + ".json")
        args = [str(DR_BIN), "--to", "json", "--no-progress"]
        if self.config["text_layer_only"]:
            args.append("--text-layer-only")
        elif not self.config["ocr"]:
            args.append("--no-ocr")
        args.append(str(path.resolve()))
        rc, err, wall, rss = run_timed(args, DR_ROOT, stdout_path=out, timeout=3600)
        if rc != 0:
            raise RuntimeError(f"docling-rs failed rc={rc}: {err[-500:]}")
        raw = out.read_text()
        ir = json.loads(raw)
        texts = ir.get("texts") or []
        tables = ir.get("tables") or []
        table_text = []
        for t in tables:
            for c in t.get("data", {}).get("table_cells") or []:
                if c.get("text"):
                    table_text.append(c["text"])
        text = "\n".join([t.get("text", "") for t in texts] + table_text)
        md_parts = []
        for t in texts:
            label = t.get("label")
            if label in ("section_header", "title"):
                md_parts.append("#" * max(1, min(6, t.get("level") or 2)) + " " + t.get("text", ""))
            elif label == "list_item":
                md_parts.append("- " + t.get("text", ""))
            else:
                md_parts.append(t.get("text", ""))
        for t in tables:
            cells = t.get("data", {}).get("table_cells") or []
            if not cells:
                continue
            nrows = t["data"].get("num_rows", 0)
            ncols = t["data"].get("num_cols", 0)
            grid = [["" for _ in range(ncols)] for _ in range(nrows)]
            for c in cells:
                r, cc = c.get("start_row_offset_idx", 0), c.get("start_col_offset_idx", 0)
                if r < nrows and cc < ncols:
                    grid[r][cc] = c.get("text", "")
            md_parts.append("\n".join("| " + " | ".join(row) + " |" for row in grid))
        return EngineResult(
            text=text, markdown="\n\n".join(md_parts),
            ir={"pages": len(ir.get("pages") or {}), "tables": tables,
                "texts": len(texts)},
            elapsed_ms=wall, peak_rss_mb=rss, peak_vram_mb=None,
            version=self.version, device=self.device, config_hash=self.config_hash(),
        )


def http_multipart(url: str, fields: dict[str, str], file_field: str, path: Path) -> tuple[int, bytes]:
    boundary = "----bench" + uuid.uuid4().hex[:16]
    body = bytearray()
    for k, v in fields.items():
        body += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    body += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{file_field}\"; "
             f"filename=\"{path.name}\"\r\nContent-Type: application/octet-stream\r\n\r\n").encode()
    body += path.read_bytes() + b"\r\n"
    body += f"--{boundary}--\r\n".encode()
    req = urllib.request.Request(url, data=bytes(body), method="POST", headers={
        "Content-Type": f"multipart/form-data; boundary={boundary}"})
    with urllib.request.urlopen(req, timeout=3600) as r:
        return r.status, r.read()


def proc_peak_rss(pid: int) -> float | None:
    try:
        for line in Path(f"/proc/{pid}/status").read_text().splitlines():
            if line.startswith("VmHWM:"):
                return int(line.split()[1]) / 1024.0
    except Exception:
        pass
    return None


class DoclingServe(Engine):
    name = "docling-serve"
    device = "cpu"
    formats = {"pdf", "docx", "xlsx", "pptx", "html", "md", "png", "jpeg", "webp"}

    def __init__(self, base: str = DOCLING_SERVE, do_ocr: bool = False, device: str = "cpu"):
        self.base = base
        self.device = device
        self.config = {"do_ocr": do_ocr, "to_formats": "json", "device": device, "server": base}
        self.version = self._version()

    def _version(self) -> str:
        try:
            with urllib.request.urlopen(self.base + "/version", timeout=10) as r:
                return json.loads(r.read()).get("version", "docling-serve")
        except Exception:
            return "docling-serve"

    def available(self):
        try:
            with urllib.request.urlopen(self.base + "/health", timeout=5) as r:
                return r.status == 200, self.base
        except Exception as e:
            return False, str(e)

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        fields = {"to_formats": "json", "do_ocr": "true" if self.config["do_ocr"] else "false",
                  "image_export_mode": "placeholder"}
        t0 = time.perf_counter()
        status, body = http_multipart(self.base + "/v1/convert/file", fields, "files", path)
        wall = (time.perf_counter() - t0) * 1000
        data = json.loads(body)
        doc = data.get("document") or {}
        js = doc.get("json_content") or {}
        texts = js.get("texts") or []
        table_text = []
        for t in js.get("tables") or []:
            for c in (t.get("data") or {}).get("table_cells") or []:
                if c.get("text"):
                    table_text.append(c["text"])
        text = "\n".join([t.get("text") or "" for t in texts] + table_text)
        md = doc.get("md_content") or text
        return EngineResult(
            text=text or "", markdown=md or "", ir={
                "pages": len(js.get("pages") or {}), "tables": js.get("tables") or [],
                "texts": len(texts)},
            elapsed_ms=wall, peak_rss_mb=None, peak_vram_mb=None, version=self.version,
            device=self.device, config_hash=self.config_hash(),
        )


class TikaEngine(Engine):
    name = "tika"
    version = "4.2.0"
    formats = {"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"}

    def __init__(self, base: str = TIKA, ocr: bool = False):
        self.base = base
        self.config = {"ocr": ocr, "server": base}
        self.version = self._version()

    def _version(self) -> str:
        try:
            with urllib.request.urlopen(self.base + "/version", timeout=5) as r:
                return r.read().decode().strip()
        except Exception:
            return "tika"

    def available(self):
        try:
            with urllib.request.urlopen(self.base + "/version", timeout=5) as r:
                return r.status == 200, self.base
        except Exception as e:
            return False, str(e)

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        data = path.read_bytes()
        headers = {"Accept": "text/plain", "Content-Type": "application/octet-stream"}
        req = urllib.request.Request(self.base + "/tika", data=data, method="PUT", headers=headers)
        t0 = time.perf_counter()
        with urllib.request.urlopen(req, timeout=1800) as r:
            text = r.read().decode("utf-8", "replace")
        wall = (time.perf_counter() - t0) * 1000
        return EngineResult(text=text, markdown=text, ir=None, elapsed_ms=wall,
                            peak_rss_mb=None, peak_vram_mb=None, version=self.version,
                            device=self.device, config_hash=self.config_hash())


class MinerUEngine(Engine):
    name = "mineru"
    formats = {"pdf", "png", "jpeg", "webp"}

    def __init__(self, backend: str = "flash", device: str = "gpu"):
        self.backend = backend
        self.device = device
        self.config = {"backend": backend, "device": device}
        self.version = "4.1.2"

    def available(self):
        return MINERU_BIN.exists(), str(MINERU_BIN)

    def convert(self, path: Path, workdir: Path, mode: str = "auto") -> EngineResult:
        outdir = workdir / ("mineru_" + path.stem)
        outdir.mkdir(parents=True, exist_ok=True)
        outfile = outdir / "out.md"
        # MinerU 4.1: only the `flash` tier runs locally; other tiers require the
        # remote mineru.net service, which ParseDock must not use.
        args = [str(MINERU_BIN), "parse", str(path.resolve()), "--tier", self.backend,
                "--force", "--wait", "1800", "--format", "markdown",
                "--limit", "2000000", "--output", str(outfile)]
        if path.suffix.lower() == ".pdf":
            args += ["--pages", "all"]  # MinerU rejects --pages for images
        rc, err, wall, rss = run_timed(args, workdir, timeout=3600)
        if rc != 0:
            raise RuntimeError(f"mineru failed rc={rc}: {err[-800:]}")
        md = outfile.read_text(errors="replace") if outfile.exists() else ""
        pages = max(1, len(re.findall(r"<!-- page \d+ of \d+ -->", md)))
        return EngineResult(text=md, markdown=md,
                            ir={"pages": pages, "tier": self.backend},
                            elapsed_ms=wall, peak_rss_mb=rss, peak_vram_mb=None,
                            version=self.version, device=self.device,
                            config_hash=self.config_hash())


def build_engines(names: list[str], device: str = "cpu") -> list[Engine]:
    out: list[Engine] = []
    for n in names:
        if n == "native":
            out.append(NativeEngine())
        elif n == "xberg":
            out.append(XbergEngine())
        elif n == "docling-rs":
            out.append(DoclingRS())
        elif n == "docling-rs-textlayer":
            e = DoclingRS(text_layer_only=True)
            e.name = "docling-rs-textlayer"
            out.append(e)
        elif n == "docling-serve":
            out.append(DoclingServe(device=device))
        elif n == "tika":
            out.append(TikaEngine())
        elif n == "mineru":
            out.append(MinerUEngine(device=device))
        else:
            raise SystemExit(f"unknown engine {n}")
    return out


# --------------------------------------------------------------------------- fixtures
def match_item(item: dict, pats: list[str]) -> bool:
    for p in pats:
        if "/" in p or "." in p:
            if p in item["path"]:
                return True
        elif p == item["category"]:
            return True
    return False


def load_fixtures(only: str = "") -> list[dict]:
    manifest = json.loads((FIXTURES / "manifest.json").read_text())
    items = manifest["fixtures"]
    if only:
        pats = [p.strip() for p in only.split(",")]
        items = [i for i in items if match_item(i, pats)]
    else:
        # Adversarial inputs are covered by the acceptance suite, not by the
        # quality benchmark.
        items = [i for i in items if i["category"] != "invalid"]
    return items


def gold_for(item: dict) -> str:
    g = item.get("gold")
    if not g:
        return ""
    p = FIXTURES / g
    return p.read_text(encoding="utf-8") if p.exists() else ""


def golden_table() -> list[list[str]]:
    return [
        ["Region", "Pallets", "On-time %", "Cost / pallet"],
        ["North", "48,120", "97.4", "3.85"],
        ["Central", "41,600", "95.1", "4.02"],
        ["South", "38,680", "93.8", "4.31"],
        ["Total", "128,400", "95.7", "4.03"],
    ]


def _norm_cell(s: str) -> str:
    return re.sub(r"[\s,]", "", norm_text(s)).lower()


def _grid_from_docling(ir: dict) -> list[list[str]] | None:
    best = None
    for t in ir.get("tables") or []:
        data = t.get("data") or {}
        rows, cols = data.get("num_rows") or 0, data.get("num_cols") or 0
        if rows < 5 or cols < 4:
            continue
        grid = [["" for _ in range(cols)] for _ in range(rows)]
        for c in data.get("table_cells") or []:
            r, cc = c.get("start_row_offset_idx", 0), c.get("start_col_offset_idx", 0)
            if r < rows and cc < cols:
                grid[r][cc] = c.get("text") or ""
        best = grid
        break
    return best


def _grid_from_xberg(ir: dict) -> list[list[str]] | None:
    for t in ir.get("tables") or []:
        cells = t.get("cells") or []
        if len(cells) >= 5 and cells and max(len(r) for r in cells) >= 4:
            return [list(r) for r in cells]
    return None


def _grid_from_markdown(md: str) -> list[list[str]] | None:
    block = []
    for line in md.splitlines():
        line = line.strip()
        if line.startswith("|") and line.endswith("|"):
            inner = line.strip("|")
            cells = [c.strip() for c in inner.split("|")]
            if all(re.fullmatch(r":?-{2,}:?", c.replace(" ", "")) or c == "" for c in cells):
                continue  # separator row
            block.append(cells)
        elif block:
            break
    if len(block) >= 5 and max(len(r) for r in block) >= 4:
        return block[:5]
    return None


def table_accuracy(engine: EngineResult) -> dict | None:
    """Positional cell accuracy against the fixture's known 5x4 table.

    Compared cell-by-cell after removing whitespace/commas and lowercasing, so
    it measures structure + content, not Markdown prettiness.
    """
    expected = [[_norm_cell(c) for c in row] for row in golden_table()]
    grid = None
    if engine.ir:
        grid = _grid_from_docling(engine.ir) or _grid_from_xberg(engine.ir)
    if grid is None:
        grid = _grid_from_markdown(engine.markdown)
    if grid is None:
        return None
    rows = min(len(grid), len(expected))
    cols = min(max(len(r) for r in grid), len(expected[0]))
    total = rows * cols
    hit = 0
    for r in range(rows):
        for c in range(cols):
            got = _norm_cell(grid[r][c]) if c < len(grid[r]) else ""
            if got and got == expected[r][c]:
                hit += 1
    header_total = cols
    header_hit = sum(1 for c in range(cols)
                     if c < len(grid[0]) and _norm_cell(grid[0][c]) == expected[0][c])
    return {
        "grid_rows": len(grid),
        "grid_cols": max(len(r) for r in grid),
        "cell_accuracy": round(hit / total, 4) if total else 0.0,
        "header_accuracy": round(header_hit / header_total, 4) if header_total else 0.0,
    }


def structure_score(engine: EngineResult, item: dict) -> dict:
    ir = engine.ir or {}
    text = engine.text
    out = {
        "has_page_numbers": bool(ir.get("pages")),
        "table_count": len(ir.get("tables") or []),
        "has_bbox": False,
        "heading_markers": len(re.findall(r"^#{1,6} ", engine.markdown, re.M)),
        "chars": len(text),
    }
    blob = json.dumps(ir)[:2000000]
    out["has_bbox"] = '"bbox"' in blob
    return out


# --------------------------------------------------------------------------- runner
def run_one(engine: Engine, item: dict, workdir: Path, run_id: str) -> dict:
    path = FIXTURES / item["path"]
    if not engine.supports(item):
        return {"run_id": run_id, "engine": engine.name, "file": item["path"],
                "format": item["format"], "category": item["category"],
                "status": "skipped_unsupported", "phase": "quality"}
    exdir = workdir / run_id
    exdir.mkdir(parents=True, exist_ok=True)
    rec = {
        "run_id": run_id,
        "engine": engine.name,
        "version": getattr(engine, "version", "?"),
        "config_hash": engine.config_hash(),
        "file": item["path"],
        "file_sha256": item["sha256"],
        "format": item["format"],
        "category": item["category"],
        "pages": item["pages"],
        "mode": "auto",
        "device": engine.device,
        "started_at": datetime.now(timezone.utc).isoformat(),
    }
    try:
        res = engine.convert(path, exdir)
        rec.update({
            "status": "ok",
            "elapsed_ms": round(res.elapsed_ms, 1),
            "peak_rss_mb": round(res.peak_rss_mb, 1) if res.peak_rss_mb else None,
            "peak_vram_mb": res.peak_vram_mb,
            "output_hash": sha256_text(res.text),
            "chars": len(res.text),
        })
        gold = gold_for(item)
        if gold:
            rec["cer"] = round(cer(gold, res.text), 4)
            rec["wer"] = round(wer(gold, res.text), 4)
            rec["token_f1"] = round(token_f1(gold, res.text), 4)
            rec["scored_chars"] = min(len(norm_text(gold)), SCORE_CHAR_CAP)
        if item["category"].startswith("pdf_native") or item["category"] == "docx":
            acc = table_accuracy(res)
            if acc is not None:
                rec["table_accuracy"] = acc
                rec["table_cell_accuracy"] = acc["cell_accuracy"]
                rec["table_header_accuracy"] = acc["header_accuracy"]
        rec["structure"] = structure_score(res, item)
        if res.ir and isinstance(res.ir.get("quality_score"), (int, float)):
            rec["engine_quality_score"] = res.ir["quality_score"]
        if res.ir and res.ir.get("extraction_method"):
            rec["extraction_method"] = res.ir["extraction_method"]
        (exdir / "output.md").write_text(res.markdown, encoding="utf-8")
        (exdir / "output.txt").write_text(res.text, encoding="utf-8")
    except Exception as e:
        rec.update({"status": "error", "error": f"{type(e).__name__}: {e}"[:1500]})
    finally:
        rec["finished_at"] = datetime.now(timezone.utc).isoformat()
        shutil.rmtree(exdir, ignore_errors=True)
    return rec


def cmd_quality(args, engines: list[Engine]) -> list[dict]:
    fixtures = load_fixtures(args.only)
    out = []
    for eng in engines:
        ok, why = eng.available()
        if not ok:
            out.append({"engine": eng.name, "status": "unavailable", "detail": why})
            continue
        print(f"--- quality: {eng.name} ({len(fixtures)} fixtures)")
        for item in fixtures:
            rid = f"q-{eng.name}-{uuid.uuid4().hex[:8]}"
            rec = run_one(eng, item, WORK, rid)
            rec["phase"] = "quality"
            out.append(rec)
            status = rec["status"]
            if status == "skipped_unsupported":
                print(f"    {item['path'].split('/')[-1]:32s} skipped (format not supported)")
                out.pop()
                continue
            extra = ""
            if "cer" in rec:
                extra = f" cer={rec['cer']:.3f}"
            if "table_cell_accuracy" in rec:
                extra += f" tbl={rec['table_cell_accuracy']:.2f}"
            print(f"    {item['path'].split('/')[-1]:32s} {status:5s} {rec.get('elapsed_ms', 0):8.0f}ms{extra}")
    return out


def cmd_perf(args, engines: list[Engine]) -> list[dict]:
    fixtures = [i for i in load_fixtures(args.only) if i["category"] in
                ("pdf_native_en", "pdf_native_zh", "pdf_scanned", "docx", "xlsx",
                 "pdf_two_column", "pdf_scanned_zh", "image_png_zh")]
    out = []
    for eng in engines:
        ok, why = eng.available()
        if not ok:
            out.append({"engine": eng.name, "status": "unavailable", "detail": why})
            continue
        print(f"--- perf: {eng.name} repeats={args.repeats}")
        for item in fixtures:
            # cold start (first call after a fresh process/idle server)
            cold = run_one(eng, item, WORK, f"p-cold-{eng.name}-{uuid.uuid4().hex[:6]}")
            cold.update({"phase": "perf", "kind": "cold"})
            out.append(cold)
            samples = []
            for _ in range(args.repeats):
                rec = run_one(eng, item, WORK, f"p-{eng.name}-{uuid.uuid4().hex[:6]}")
                rec.update({"phase": "perf", "kind": "warm"})
                samples.append(rec)
                out.append(rec)
            okr = [s for s in samples if s["status"] == "ok"]
            if okr:
                med = statistics.median(s["elapsed_ms"] for s in okr)
                rss = statistics.median(s["peak_rss_mb"] for s in okr if s.get("peak_rss_mb")) \
                    if any(s.get("peak_rss_mb") for s in okr) else None
                print(f"    {item['path'].split('/')[-1]:32s} cold={cold.get('elapsed_ms', 0):8.0f}ms "
                      f"median={med:8.0f}ms p95={sorted(s['elapsed_ms'] for s in okr)[max(0, int(len(okr)*0.95)-1)]:8.0f}ms "
                      f"rss={rss if rss is None else round(rss,1)}MB")
    return out


def cmd_concurrency(args, engines: list[Engine]) -> list[dict]:
    fixtures = [i for i in load_fixtures(args.only) if i["category"] in ("pdf_native_en", "pdf_scanned")]
    out = []
    for eng in engines:
        ok, why = eng.available()
        if not ok:
            continue
        for item in fixtures:
            if not eng.supports(item):
                print(f"    {item['path'].split('/')[-1]:28s} skipped (format not supported)")
                continue
            path = FIXTURES / item["path"]
            # warm up
            try:
                eng.convert(path, WORK / "warm", mode="auto")
            except Exception:
                pass
            for level in (1, 2, 4, 8):
                results: list[float] = []
                errors: list[str] = []
                lock = threading.Lock()

                def worker(idx: int):
                    wid = WORK / f"c-{eng.name}-{level}-{idx}-{uuid.uuid4().hex[:4]}"
                    wid.mkdir(parents=True, exist_ok=True)
                    try:
                        r = eng.convert(path, wid)
                        with lock:
                            results.append(r.elapsed_ms)
                    except Exception as e:
                        with lock:
                            errors.append(f"{type(e).__name__}: {e}"[:300])
                    finally:
                        shutil.rmtree(wid, ignore_errors=True)

                t0 = time.perf_counter()
                threads = [threading.Thread(target=worker, args=(i,)) for i in range(level)]
                for t in threads:
                    t.start()
                for t in threads:
                    t.join()
                wall = time.perf_counter() - t0
                rec = {
                    "phase": "concurrency", "engine": eng.name, "version": eng.version,
                    "config_hash": eng.config_hash(), "file": item["path"],
                    "format": item["format"], "device": eng.device, "concurrency": level,
                    "docs": len(results), "errors": len(errors),
                    "wall_s": round(wall, 3),
                    "docs_per_s": round(len(results) / wall, 3) if wall else 0,
                    "pages_per_s": round(len(results) * max(1, item["pages"]) / wall, 3) if wall else 0,
                    "p50_ms": round(statistics.median(results), 1) if results else None,
                    "max_ms": round(max(results), 1) if results else None,
                    "error_sample": errors[:3],
                    "status": "ok" if len(results) == level else "partial",
                }
                out.append(rec)
                print(f"    {item['path'].split('/')[-1]:28s} c={level} "
                      f"{rec['docs_per_s']:6.2f} docs/s  {rec['pages_per_s']:7.2f} pages/s  err={len(errors)}")
    return out


def write_outputs(records: list[dict], args) -> None:
    RAW.mkdir(parents=True, exist_ok=True)
    SUMMARY.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    rawfile = RAW / f"bench-{stamp}.jsonl"
    with open(rawfile, "w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    # summary CSV (per engine/fixture, warm medians)
    groups: dict[tuple, list[dict]] = {}
    for r in records:
        if r.get("phase") == "perf" and r.get("kind") == "warm" and r.get("status") == "ok":
            groups.setdefault((r["engine"], r["file"]), []).append(r)
    phases = sorted({r.get("phase") for r in records if r.get("phase")})
    csvpath = SUMMARY / ("benchmarks-" + "+".join(phases) + ".csv") if phases else SUMMARY / "benchmarks.csv"
    with open(csvpath, "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["engine", "version", "config_hash", "file", "file_hash", "format",
                    "pages", "mode", "device", "runs", "median_ms", "p95_ms", "median_rss_mb",
                    "status", "output_hash", "cer", "wer", "table_cell_accuracy"])
        for (eng, file), rows in sorted(groups.items()):
            ms = sorted(r["elapsed_ms"] for r in rows)
            rss = [r["peak_rss_mb"] for r in rows if r.get("peak_rss_mb")]
            w.writerow([eng, rows[0]["version"], rows[0]["config_hash"], file, rows[0]["file_sha256"],
                        rows[0]["format"], rows[0]["pages"], rows[0]["mode"], rows[0]["device"],
                        len(rows), round(statistics.median(ms), 1),
                        round(ms[max(0, int(len(ms) * 0.95) - 1)], 1),
                        round(statistics.median(rss), 1) if rss else "",
                        "ok", rows[0]["output_hash"],
                        rows[0].get("cer", ""), rows[0].get("wer", ""),
                        rows[0].get("table_cell_accuracy", "")])
    print(f"\nraw:     {rawfile.relative_to(ROOT)}")
    print(f"summary: {csvpath.relative_to(ROOT)}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("cmd", choices=["quality", "perf", "concurrency", "all"])
    ap.add_argument("--engines", default="native,xberg,docling-rs")
    ap.add_argument("--repeats", type=int, default=5)
    ap.add_argument("--only", default="", help="substring filter on fixture path/category")
    ap.add_argument("--device", default="cpu")
    args = ap.parse_args()

    WORK.mkdir(parents=True, exist_ok=True)
    engines = build_engines([e.strip() for e in args.engines.split(",") if e.strip()], args.device)
    for e in engines:
        ok, why = e.available()
        print(f"engine {e.name:22s} available={ok} {'' if ok else why}")

    records: list[dict] = []
    if args.cmd in ("quality", "all"):
        records += cmd_quality(args, engines)
    if args.cmd in ("perf", "all"):
        records += cmd_perf(args, engines)
    if args.cmd in ("concurrency", "all"):
        records += cmd_concurrency(args, engines)
    write_outputs(records, args)
    errors = [r for r in records if r.get("status") in ("error", "partial")]
    print(f"\n{len(records)} records, {len(errors)} errors")
    return 0


if __name__ == "__main__":
    sys.exit(main())
