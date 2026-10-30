#!/usr/bin/env python3
"""Classify the licence audit result for the two images (PR-01, requirement 7).

    python3 scripts/classify_licence.py control-sbom.json worker-sbom.json

Prints a stable, greppable classification:

    control_plane=PASS|BLOCK
    worker=PASS|BLOCK
    worker_gpl=[...]

Exit code:
    0  the control plane is GPL-free (the worker may be BLOCKED; that is recorded
       but is not a CI failure — external distribution is gated in release-gate)
    1  the control plane carries a GPL library (never acceptable)
    2  a SBOM file is missing or unreadable
"""
from __future__ import annotations

import json
import sys
from pathlib import Path


def load(path: str) -> dict:
    try:
        return json.loads(Path(path).read_text())
    except (OSError, json.JSONDecodeError) as e:
        print(f"cannot read SBOM {path}: {e}", file=sys.stderr)
        raise SystemExit(2)


def main() -> int:
    if len(sys.argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    control = load(sys.argv[1])
    worker = load(sys.argv[2])

    control_block = bool(control.get("gpl_libraries"))
    print(f"control_plane={'BLOCK' if control_block else 'PASS'}")
    print(f"control_plane_packages={control.get('packages_count', '?')}")
    print(f"control_plane_lgpl={len(control.get('lgpl_libraries') or [])}")
    print(f"worker={'BLOCK' if worker.get('gpl_libraries') else 'PASS'}")
    print(f"worker_gpl={worker.get('gpl_libraries') or []}")
    print(f"worker_lgpl={len(worker.get('lgpl_libraries') or [])}")
    for note in worker.get("notes") or []:
        print(f"note: {note}")

    if worker.get("gpl_libraries"):
        print("verdict: worker image must not be published until L1 is signed off")
    if control_block:
        print("verdict: control plane must stay GPL-free — blocking", file=sys.stderr)
        return 1
    print("verdict: control plane is clean")
    return 0


if __name__ == "__main__":
    sys.exit(main())
