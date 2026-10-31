#!/usr/bin/env python3
"""Compare OCR backends on the same fixtures, gold set and timing method.

Every backend is driven exactly the way ParseDock's adapters drive it, except
where a flag is named explicitly in the table (``--acceleration``, OCR mode),
so the numbers answer "what would this engine do in our worker?" rather than
"what could it do with a hand-tuned pipeline?".

Scoring
-------
* **CER** — edit distance over characters after removing all whitespace and
  normalising to NFC. Chinese has no word boundaries, so CER is the honest
  metric for both languages; lowercase is *not* folded.
* **digit CER** — the same distance over digits only. Amounts, dates and
  invoice numbers are what a reviewer actually checks.
* **WER** — whitespace tokens with punctuation stripped; reported for English
  fixtures only (meaningless for Chinese).

Latency is wall-clock for one whole document (process start to exit), so it
includes the model load that a cold worker pays. Peak RSS comes from
``/usr/bin/time -f %M`` and covers the whole process tree.

Usage
-----
    python3 benchmark/scripts/ocr_bench.py --runs 5 --json benchmark/results/ocr-cpu.json
    python3 benchmark/scripts/ocr_bench.py --engines xberg-paddle-zh,xberg-paddle-zh-cuda

Backends that need a virtualenv (RapidOCR) are launched with
``--rapidocr-python`` (default: ``/tmp/ocrbench/venv/bin/python``); they are
skipped when that interpreter or the package is missing.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import statistics
import subprocess
import sys
import time
import unicodedata
from dataclasses import dataclass, field
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "benchmark" / "fixtures" / "ocr"
XBERG = REPO / "runtime" / "engines" / "xberg-cli" / "xberg"
XBERG_LIB = REPO / "runtime" / "engines" / "xberg-cli"
DOCLING_RS = REPO / "runtime" / "engines" / "docling-rs" / "docling-rs"
TIME = "/usr/bin/time"

ENVELOPE = "--- extraction envelope ---"


# --------------------------------------------------------------------------
# backends
# --------------------------------------------------------------------------
@dataclass
class Engine:
    name: str
    argv: list[str] = field(default_factory=list)
    note: str = ""
    env: dict[str, str] = field(default_factory=dict)
    kind: str = "cmd"  # cmd | rapidocr
    params: dict[str, str] = field(default_factory=dict)

    def available(self, rapidocr_python: str) -> bool:
        if self.kind == "rapidocr":
            return Path(rapidocr_python).exists()
        exe = self.argv[0]
        return Path(exe).exists() or shutil.which(exe) is not None


def xberg(backend: str, lang: str, accel: str = "", extra: list[str] | None = None) -> list[str]:
    argv = [
        str(XBERG), "extract", "--no-config-discovery", "--no-cache", "true",
        "--format", "text", "--ocr", "true",
        "--ocr-backend", backend, "--ocr-language", lang,
    ]
    if accel:
        argv += ["--acceleration", accel]
    return argv + (extra or [])


def docling(lang: str, extra: list[str] | None = None, env: dict[str, str] | None = None) -> Engine:
    argv = [str(DOCLING_RS), "--to", "md", "--ocr-lang", lang] + (extra or [])
    return Engine(name="", argv=argv, env=env or {})


def engines(rapidocr_python: str) -> list[Engine]:
    tesseract = shutil.which("tesseract") or "tesseract"
    out = [
        Engine("xberg-tesseract-eng", xberg("tesseract", "eng"), "xberg, tesseract eng"),
        Engine("xberg-tesseract-chi", xberg("tesseract", "chi_sim"), "xberg, tesseract chi_sim"),
        Engine("xberg-paddle-en", xberg("paddle-ocr", "en"), "xberg PP-OCRv6 small, english family"),
        Engine("xberg-paddle-ch", xberg("paddle-ocr", "ch"), "xberg PP-OCRv6 small, chinese family"),
        Engine("xberg-paddle-ch-cuda", xberg("paddle-ocr", "ch", "cuda"), "same + --acceleration cuda (expected to fail on the release build)"),
        Engine("xberg-tesseract-eng-cuda", xberg("tesseract", "eng", "cuda"), "tesseract is not an ONNX model: acceleration is irrelevant"),
    ]
    for lang in ("en", "ch"):
        d = docling(lang)
        d.name = f"doclingrs-ppocr-{lang}"
        d.note = f"docling-rs default EP (CPU), --ocr-lang {lang}"
        out.append(d)
        d2 = docling(lang, extra=["--ocr-mode", "full_page"])
        d2.name = f"doclingrs-ppocr-{lang}-fullpage"
        d2.note = "docling-rs --ocr-mode full_page"
        out.append(d2)
    d3 = docling("ch", env={"DOCLING_RS_EP": "cuda"})
    d3.name = "doclingrs-ppocr-ch-epcuda"
    d3.note = "DOCLING_RS_EP=cuda on the CPU-only build (silent fallback?)"
    out.append(d3)
    # Standalone Tesseract on the rendered page: isolates rasterisation from
    # the engine's own PDF handling.
    for lang in ("eng", "chi_sim"):
        e = Engine(f"tesseract-cli-{lang}", [], "system tesseract on the 300 dpi PNG")
        e.kind = "cmd"
        e.argv = [tesseract, "{image}", "stdout", "-l", lang]
        out.append(e)
    for tier in ("small", "medium"):
        e = Engine(
            f"rapidocr-v6-{tier}",
            [],
            "standalone rapidocr 3.10, PP-OCRv6 " + tier + " (ch dict)",
            kind="rapidocr",
            params={"tier": tier},
        )
        out.append(e)
    return out


# --------------------------------------------------------------------------
# running
# --------------------------------------------------------------------------
RAPIDOCR_RUNNER = r"""
import json, sys, time
from rapidocr import RapidOCR
from rapidocr.utils.typings import ModelType

path, tier = sys.argv[1], sys.argv[2]
mt = ModelType(tier)
params = {"Det.model_type": mt, "Rec.model_type": mt, "Det.lang_type": "ch", "Rec.lang_type": "ch"}
t0 = time.time()
engine = RapidOCR(params=params)
res = engine(path)
txt = "\n".join(res.txts or [])
print(json.dumps({"text": txt, "elapsed": time.time() - t0}))
"""


def run_once(engine: Engine, fixture_pdf: Path, fixture_png: Path, rapidocr_python: str, timeout: int):
    image_fixture = engine.name.startswith(("tesseract-cli", "rapidocr"))
    target = fixture_png if image_fixture else fixture_pdf
    if engine.kind == "rapidocr":
        argv = [rapidocr_python, "-c", RAPIDOCR_RUNNER, str(target), engine.params["tier"]]
        env = dict(os.environ, HOME="/tmp/ocrbench/home")
    else:
        argv = [a.replace("{image}", str(target)) for a in engine.argv] + [str(target)]
        env = dict(os.environ, LD_LIBRARY_PATH=str(XBERG_LIB), **engine.env)
        if Path(TIME).exists():
            argv = [TIME, "-f", "MAXRSS=%M", *argv]
    t0 = time.time()
    proc = subprocess.run(
        argv, capture_output=True, text=True, errors="replace", timeout=timeout, env=env
    )
    wall_ms = (time.time() - t0) * 1000
    stderr = proc.stderr or ""
    rss = 0
    for line in stderr.splitlines():
        if line.startswith("MAXRSS="):
            rss = int(line.split("=", 1)[1]) // 1024  # KiB -> MiB
    if engine.kind == "rapidocr" and proc.returncode == 0:
        try:
            text = json.loads(proc.stdout.strip().splitlines()[-1])["text"]
        except Exception:
            text = ""
    else:
        text = proc.stdout
    if ENVELOPE in text:
        text = text.split(ENVELOPE)[0]
    return {
        "rc": proc.returncode,
        "wall_ms": wall_ms,
        "rss_mib": rss,
        "text": text,
        "stderr_tail": "\n".join(stderr.strip().splitlines()[-3:]),
    }


# --------------------------------------------------------------------------
# scoring
# --------------------------------------------------------------------------
def norm(s: str) -> str:
    return unicodedata.normalize("NFC", s)


def squash(s: str) -> str:
    return "".join(norm(s).split())


def levenshtein(a: str, b: str) -> int:
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


def cer(hyp: str, gold: str) -> float:
    h, g = squash(hyp), squash(gold)
    if not g:
        return 0.0
    return levenshtein(h, g) / len(g)


def digit_cer(hyp: str, gold: str) -> float:
    h = "".join(ch for ch in norm(hyp) if ch.isdigit())
    g = "".join(ch for ch in norm(gold) if ch.isdigit())
    if not g:
        return 0.0
    return levenshtein(h, g) / len(g)


def wer(hyp: str, gold: str) -> float:
    import re

    def toks(s: str) -> list[str]:
        s = re.sub(r"[^\w\s]", " ", norm(s).lower())
        return s.split()

    h, g = toks(hyp), toks(gold)
    if not g:
        return 0.0
    # token-level distance
    prev = list(range(len(h) + 1))
    for i, gt in enumerate(g, 1):
        cur = [i]
        for j, ht in enumerate(h, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (gt != ht)))
        prev = cur
    return prev[-1] / len(g)


def pct(values: list[float], q: float) -> float:
    if not values:
        return 0.0
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    k = (len(ordered) - 1) * q
    lo, hi = int(k), min(int(k) + 1, len(ordered) - 1)
    return ordered[lo] + (ordered[hi] - ordered[lo]) * (k - lo)


# --------------------------------------------------------------------------
def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixtures", default="en_dense,zh_dense")
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--engines", default="all")
    ap.add_argument("--rapidocr-python", default="/tmp/ocrbench/venv/bin/python")
    ap.add_argument("--timeout", type=int, default=900)
    ap.add_argument("--json", default="")
    ap.add_argument("--md", default="")
    ap.add_argument("--label", default="cpu", help="tag written into the JSON (cpu, cuda, ...)")
    args = ap.parse_args()

    all_engines = engines(args.rapidocr_python)
    if args.engines != "all":
        wanted = {e.strip() for e in args.engines.split(",")}
        all_engines = [e for e in all_engines if e.name in wanted]
    print(f"{len(all_engines)} engine(s), {args.runs} run(s) per fixture, label={args.label}", file=sys.stderr)

    rows = []
    for fixture in [f.strip() for f in args.fixtures.split(",") if f.strip()]:
        pdf = FIXTURES / f"{fixture}.pdf"
        png = FIXTURES / f"{fixture}.png"
        if not pdf.exists():
            print(f"missing fixture {pdf}", file=sys.stderr)
            continue
        lang = "en" if fixture.startswith("en") else "zh"
        for engine in all_engines:
            # A language-specific backend on the wrong fixture is a control, not
            # a mistake: it shows what a misconfigured worker would emit.
            if not engine.available(args.rapidocr_python):
                print(f"  skip {engine.name} (not installed)", file=sys.stderr)
                continue
            gold = (FIXTURES / f"{fixture}.p1.gold.txt" if engine.name.startswith(("tesseract-cli", "rapidocr")) else FIXTURES / f"{fixture}.gold.txt").read_text(encoding="utf-8")
            walls, rss, texts, rcs = [], [], [], []
            for _ in range(args.runs):
                try:
                    r = run_once(engine, pdf, png, args.rapidocr_python, args.timeout)
                except subprocess.TimeoutExpired:
                    rcs.append("timeout")
                    walls.append(args.timeout * 1000)
                    texts.append("")
                    rss.append(0)
                    break
                rcs.append(r["rc"])
                walls.append(r["wall_ms"])
                rss.append(r["rss_mib"])
                texts.append(r["text"])
            ok = all(rc == 0 for rc in rcs)
            best = texts[-1] if texts else ""
            row = {
                "engine": engine.name,
                "note": engine.note,
                "fixture": fixture,
                "lang": lang,
                "runs": len(walls),
                "ok": ok,
                "rc": rcs,
                "p50_ms": round(statistics.median(walls), 1) if walls else 0,
                "p95_ms": round(pct(walls, 0.95), 1),
                "min_ms": round(min(walls), 1) if walls else 0,
                "rss_mib": max(rss) if rss else 0,
                "cer": round(cer(best, gold), 4) if ok else None,
                "digit_cer": round(digit_cer(best, gold), 4) if ok else None,
                "wer": round(wer(best, gold), 4) if ok and lang == "en" else None,
                "chars": len(squash(best)),
                "gold_chars": len(squash(gold)),
                "label": args.label,
            }
            rows.append(row)
            state = "ok " if ok else "FAIL"
            cer_s = "-" if row["cer"] is None else f"{row['cer']:.3f}"
            dig_s = "-" if row["digit_cer"] is None else f"{row['digit_cer']:.3f}"
            print(
                f"  {state} {engine.name:<28} {fixture:<9} "
                f"p50={row['p50_ms']:>8.0f}ms p95={row['p95_ms']:>8.0f}ms "
                f"CER={cer_s} dig={dig_s} rss={row['rss_mib']}MiB",
                file=sys.stderr,
            )
            if not ok:
                print(f"        {r['stderr_tail'][:300]}", file=sys.stderr)

    if args.json:
        Path(args.json).parent.mkdir(parents=True, exist_ok=True)
        Path(args.json).write_text(json.dumps(rows, indent=1) + "\n", encoding="utf-8")
    if args.md:
        Path(args.md).parent.mkdir(parents=True, exist_ok=True)
        lines = [
            "| engine | fixture | ok | p50 ms | p95 ms | peak RSS MiB | CER | digit CER | WER |",
            "| --- | --- | --- | --- | --- | --- | --- | --- | --- |",
        ]
        for r in rows:
            def f(v, dash="—"):
                return dash if v is None else (f"{v:.3f}" if isinstance(v, float) else str(v))

            lines.append(
                f"| `{r['engine']}` | {r['fixture']} | {'yes' if r['ok'] else 'NO'} | {r['p50_ms']:.0f} | "
                f"{r['p95_ms']:.0f} | {r['rss_mib']} | {f(r['cer'])} | {f(r['digit_cer'])} | {f(r['wer'])} |"
            )
        Path(args.md).write_text("\n".join(lines) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
