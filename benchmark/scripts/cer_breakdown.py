#!/usr/bin/env python3
"""Explain a CER number instead of only reporting it.

`ocr_bench.py` removes whitespace before scoring, so a claim like "the 7 % CER is
mostly inserted spaces" is *testable*: if it were true, the whitespace-sensitive
score would be far worse than the whitespace-insensitive one. This script
produces both, then classifies every edit as an insertion, deletion or
substitution and lists the most frequent confusions.

No document content is printed beyond what the synthetic fixture already
contains; the confusion table is a count-plus-glyph summary, not a transcript.

Usage:
    python3 benchmark/scripts/cer_breakdown.py --engine xberg-tesseract-chi --fixture zh_dense
    python3 benchmark/scripts/cer_breakdown.py --text-file out.txt --fixture zh_dense
    python3 benchmark/scripts/cer_breakdown.py --engine xberg-paddle-ch --fixture zh_dense \
        --json benchmark/results/cer-breakdown.json
"""

from __future__ import annotations

import argparse
import collections
import importlib.util
import json
import re
import sys
import unicodedata
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "benchmark" / "fixtures" / "ocr"


def load_bench():
    spec = importlib.util.spec_from_file_location("ocr_bench", REPO / "benchmark" / "scripts" / "ocr_bench.py")
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


def norm(s: str) -> str:
    return unicodedata.normalize("NFC", s)


def classify(hyp: str, gold: str) -> dict:
    """Levenshtein alignment with per-operation counts and confusion pairs."""
    h, g = norm(hyp), norm(gold)
    n, m = len(h), len(g)
    # full DP matrix (fixtures are ~2 k chars, so this is fine)
    dp = [[0] * (m + 1) for _ in range(n + 1)]
    for i in range(1, n + 1):
        dp[i][0] = i
    for j in range(1, m + 1):
        dp[0][j] = j
    for i in range(1, n + 1):
        hi = h[i - 1]
        for j in range(1, m + 1):
            cost = 0 if hi == g[j - 1] else 1
            dp[i][j] = min(dp[i - 1][j] + 1, dp[i][j - 1] + 1, dp[i - 1][j - 1] + cost)
    subs: collections.Counter = collections.Counter()
    ins: collections.Counter = collections.Counter()
    dels: collections.Counter = collections.Counter()
    i, j = n, m
    while i > 0 or j > 0:
        if i > 0 and j > 0 and dp[i][j] == dp[i - 1][j - 1] + (0 if h[i - 1] == g[j - 1] else 1):
            if h[i - 1] != g[j - 1]:
                subs[(g[j - 1], h[i - 1])] += 1
            i -= 1
            j -= 1
        elif i > 0 and dp[i][j] == dp[i - 1][j] + 1:
            ins[h[i - 1]] += 1
            i -= 1
        else:
            dels[g[j - 1]] += 1
            j -= 1
    distance = dp[n][m]
    return {
        "distance": distance,
        "substitutions": sum(subs.values()),
        "insertions": sum(ins.values()),
        "deletions": sum(dels.values()),
        "top_substitutions": [[a, b, c] for (a, b), c in subs.most_common(10)],
        "top_insertions": [[a, b] for a, b in ins.most_common(10)],
        "top_deletions": [[a, b] for a, b in dels.most_common(10)],
    }


def score(hyp: str, gold: str) -> dict:
    """CER two ways: whitespace-insensitive (what ocr_bench reports) and raw."""
    def sq(s: str) -> str:
        return "".join(norm(s).split())

    def raw(s: str) -> str:
        # keep line structure, collapse runs of spaces to one: this is the
        # closest "same text, different spacing" baseline
        return "\n".join(re.sub(r"[ \t]+", " ", line).strip() for line in norm(s).splitlines() if line.strip())

    h_sq, g_sq = sq(hyp), sq(gold)
    h_raw, g_raw = raw(hyp), raw(gold)
    out = {
        "gold_chars": len(g_sq),
        "hyp_chars": len(h_sq),
        "cer_squashed": (classify(h_sq, g_sq)["distance"] / len(g_sq)) if g_sq else 0.0,
        "cer_preserving_lines": (classify(h_raw, g_raw)["distance"] / len(g_raw)) if g_raw else 0.0,
    }
    out["squashed_detail"] = classify(h_sq, g_sq)
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--engine", default="", help="engine name from ocr_bench.py (runs it once)")
    ap.add_argument("--text-file", default="", help="use this hypothesis instead of running an engine")
    ap.add_argument("--fixture", default="zh_dense")
    ap.add_argument("--json", default="")
    args = ap.parse_args()

    fixture = args.fixture
    gold_path = FIXTURES / f"{fixture}.gold.txt"
    if not gold_path.exists():
        raise SystemExit(f"missing gold {gold_path}")
    gold = gold_path.read_text(encoding="utf-8")

    if args.text_file:
        hyp = Path(args.text_file).read_text(encoding="utf-8")
        engine = args.text_file
    elif args.engine:
        bench = load_bench()
        eng = next((e for e in bench.engines("/tmp/ocrbench/venv/bin/python") if e.name == args.engine), None)
        if eng is None:
            raise SystemExit(f"unknown engine {args.engine}")
        r = bench.run_once(eng, FIXTURES / f"{fixture}.pdf", FIXTURES / f"{fixture}.png", "/tmp/ocrbench/venv/bin/python", 900)
        if r["rc"] != 0:
            raise SystemExit(f"engine failed: {r['stderr_tail'][:300]}")
        hyp = r["text"]
        engine = args.engine
    else:
        raise SystemExit("need --engine or --text-file")

    res = score(hyp, gold)
    res["engine"] = engine
    res["fixture"] = fixture
    print(f"{engine} on {fixture}")
    print(f"  CER (whitespace removed, as reported): {res['cer_squashed']:.3f}")
    print(f"  CER (line structure kept)            : {res['cer_preserving_lines']:.3f}")
    d = res["squashed_detail"]
    print(f"  edits: {d['substitutions']} substitutions, {d['insertions']} insertions, {d['deletions']} deletions")
    print("  top substitutions (gold -> hypothesis, count):")
    for a, b, c in d["top_substitutions"][:6]:
        print(f"    {a!r} -> {b!r}  x{c}")
    if d["top_insertions"]:
        print("  top insertions:", ", ".join(f"{a!r} x{b}" for a, b in d["top_insertions"][:6]))
    if d["top_deletions"]:
        print("  top deletions :", ", ".join(f"{a!r} x{b}" for a, b in d["top_deletions"][:6]))
    if args.json:
        Path(args.json).parent.mkdir(parents=True, exist_ok=True)
        Path(args.json).write_text(json.dumps(res, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
        print(f"wrote {args.json}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
