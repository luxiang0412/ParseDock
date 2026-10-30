#!/usr/bin/env python3
"""Engine quality gate (PR-11).

Compares the current benchmark summary against a committed baseline and fails when

* a quality dimension drops more than ``--max-quality-drop`` (relative), or
* a latency percentile regresses more than ``--max-latency-regress`` (relative),

for any engine that the baseline covers. The rule is deliberately relative: absolute
numbers differ per host, relative drift does not.

    python benchmark/scripts/gate.py --update    # record/refresh the baseline
    python benchmark/scripts/gate.py             # check (CI)

Exit codes: 0 pass (or baseline written), 1 regression, 2 missing data.
"""
from __future__ import annotations

import argparse
import csv
import datetime
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SUMMARY = ROOT / "benchmark" / "results" / "summary"
BASELINE = ROOT / "benchmark" / "results" / "baseline.json"


def _rel(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def load_scores() -> dict:
    path = SUMMARY / "scores.json"
    if not path.exists():
        return {}
    return json.loads(path.read_text())


def load_quality() -> dict:
    """Per-file CER and table accuracy, keyed by engine/file."""
    path = SUMMARY / "benchmarks-quality.csv"
    if not path.exists():
        return {}
    out = {}
    with path.open(newline="") as fh:
        for row in csv.DictReader(fh):
            eng = row.get("engine")
            f = row.get("file")
            if not eng or not f:
                continue
            out.setdefault(eng, {})[f] = row
    return out


def load_perf() -> dict:
    """Per-file latency, keyed by engine/file."""
    path = SUMMARY / "benchmarks-perf.csv"
    if not path.exists():
        return {}
    out = {}
    with path.open(newline="") as fh:
        for row in csv.DictReader(fh):
            eng = row.get("engine")
            f = row.get("file")
            if not eng or not f:
                continue
            out.setdefault(eng, {})[f] = row
    return out


def snapshot() -> dict:
    quality = load_quality()
    perf = load_perf()
    scores = load_scores()
    snap = {"engines": {}, "samples": {}}
    for e in scores.get("engines", []):
        name = e.get("engine")
        snap["engines"][name] = {
            "quality": e.get("dimensions", {}).get("quality"),
            "structure": e.get("quality_sub", {}).get("structure"),
            "performance": e.get("dimensions", {}).get("performance"),
        }
    for eng, files in quality.items():
        for f, row in files.items():
            cer = row.get("cer")
            if cer not in (None, ""):
                snap["samples"].setdefault(eng, {})[f] = {"cer": float(cer)}
    for eng, files in perf.items():
        for f, row in files.items():
            for key in ("p50_ms", "p95_ms"):
                v = row.get(key)
                if v in (None, ""):
                    continue
                snap["samples"].setdefault(eng, {}).setdefault(f, {})[key] = float(v)
    return snap


def rel_drop(base: float, cur: float) -> float:
    """Positive number = how much cur is worse than base, relative to base."""
    if base <= 0:
        return 0.0
    return (base - cur) / base


def rel_rise(base: float, cur: float) -> float:
    if base <= 0:
        return 0.0
    return (cur - base) / base


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--update", action="store_true", help="write the current summary as the baseline")
    ap.add_argument("--max-quality-drop", type=float, default=0.02)
    ap.add_argument("--max-latency-regress", type=float, default=0.15)
    args = ap.parse_args()

    cur = snapshot()
    if not cur.get("engines") and not cur.get("samples"):
        print("gate: no benchmark summary; run `make bench && make score` first", file=sys.stderr)
        return 2

    if args.update:
        BASELINE.parent.mkdir(parents=True, exist_ok=True)
        stamp = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
        BASELINE.write_text(json.dumps({"generated_at": stamp, **cur}, indent=2, sort_keys=True) + "\n")
        print(f"gate: baseline written to {_rel(BASELINE)}")
        return 0

    if not BASELINE.exists():
        print(f"gate: no baseline at {_rel(BASELINE)}; run with --update", file=sys.stderr)
        return 2
    base = json.loads(BASELINE.read_text())

    failures, warnings = [], []
    for name, b in base.get("engines", {}).items():
        c = cur["engines"].get(name)
        if not c:
            warnings.append(f"{name}: missing from the current run (skipped)")
            continue
        for dim in ("quality", "structure"):
            bv, cv = b.get(dim), c.get(dim)
            if bv is None or cv is None:
                continue
            d = rel_drop(bv, cv)
            if d > args.max_quality_drop:
                failures.append(f"{name}.{dim}: {bv:.3f} → {cv:.3f} ({d * 100:+.1f} % > "
                                f"{args.max_quality_drop * 100:.0f} %)")

    for eng, files in base.get("samples", {}).items():
        for f, b in files.items():
            c = cur.get("samples", {}).get(eng, {}).get(f)
            if not c:
                continue
            if "cer" in b and "cer" in c:
                d = (c["cer"] - b["cer"])  # absolute CER points
                if d > 0.02:
                    failures.append(f"{eng}/{f}: CER {b['cer']:.4f} → {c['cer']:.4f} (+{d:.4f} points)")
            for key in ("p50_ms", "p95_ms"):
                if key in b and key in c:
                    r = rel_rise(b[key], c[key])
                    if r > args.max_latency_regress:
                        failures.append(f"{eng}/{f}: {key} {b[key]:.1f} → {c[key]:.1f} ms "
                                        f"({r * 100:+.1f} % > {args.max_latency_regress * 100:.0f} %)")

    for w in warnings:
        print(f"gate: warn: {w}")
    if failures:
        print("gate: FAIL", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        print("\nAn engine change that regresses quality >2 % or latency >15 % needs an ADR "
              "(docs/DECISIONS.md) and an explicit baseline update.", file=sys.stderr)
        return 1
    print("gate: pass")
    return 0


if __name__ == "__main__":
    sys.exit(main())
