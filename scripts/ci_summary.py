#!/usr/bin/env python3
"""Aggregate CI job results into a merge gate (PR-01, requirement 3).

Usage:
    python3 scripts/ci_summary.py static=success unit=failure ...  [--github-summary FILE]

Prints a markdown table and exits:
    0  every required job succeeded
    1  at least one job did not succeed (failure, cancelled, skipped, empty)

Why a script: the first implementation used
`echo "$RESULTS" | grep -qv '=success'`, which is a substring match — the line
"contract=failure" also contains "=success" (from another pair), so the summary
passed while the contract job had failed. That is a false green in the merge gate;
this module is unit-tested in tests/unit/test_ci_summary.py.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

# The aggregator cannot report on itself: these are the jobs it gates.
REQUIRED = ("static", "unit", "contract", "lifecycle", "image")


def parse(pairs: list[str]) -> dict[str, str]:
    out: dict[str, str] = {}
    for raw in pairs:
        for token in raw.split():
            token = token.strip()
            if not token:
                continue
            if "=" not in token:
                out[token] = "unknown"
                continue
            name, result = token.split("=", 1)
            out[name.strip()] = result.strip().lower()
    return out


def evaluate(results: dict[str, str]) -> tuple[bool, list[str]]:
    problems: list[str] = []
    for name in REQUIRED:
        if name not in results:
            problems.append(f"{name}: missing result (job did not report)")
        elif results[name] != "success":
            problems.append(f"{name}: {results[name]}")
    return not problems, problems


def render(results: dict[str, str], problems: list[str]) -> str:
    lines = ["## PR-01 CI summary", "", "| job | result |", "| --- | --- |"]
    for name in REQUIRED:
        lines.append(f"| {name} | {results.get(name, 'missing')} |")
    if problems:
        lines += ["", "**Blocking:**", ""]
        lines += [f"- {p}" for p in problems]
    else:
        lines += ["", "All required jobs succeeded."]
    return "\n".join(lines) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("results", nargs="*")
    ap.add_argument("--github-summary", default="")
    args = ap.parse_args()

    results = parse(args.results)
    ok, problems = evaluate(results)
    text = render(results, problems)
    print(text)
    if args.github_summary:
        with open(args.github_summary, "a", encoding="utf-8") as f:
            f.write(text)
    if not ok:
        for p in problems:
            print(f"::error::{p}", file=sys.stderr)
        print("one or more required jobs did not succeed", file=sys.stderr)
        return 1
    print("all required jobs succeeded")
    return 0


if __name__ == "__main__":
    sys.exit(main())
