#!/usr/bin/env python3
"""Assert a single version across every artefact (PR-10).

The service, the OpenAPI document, both SDKs and the Java pom must move together; a
release that ships 0.1.0 binaries with a 0.1.0 client generated from a 0.2.0 document is a
contract break, not a version bump.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def openapi_version() -> str:
    m = re.search(r"^  version:\s*(\S+)\s*$", read(ROOT / "api/openapi.yaml"), re.M)
    if not m:
        raise SystemExit("api/openapi.yaml has no info.version")
    return m.group(1)


def python_version() -> str:
    m = re.search(r'^__version__\s*=\s*"([^"]+)"', read(ROOT / "sdk/python/parsedock_sdk/__init__.py"), re.M)
    m2 = re.search(r'^version\s*=\s*"([^"]+)"', read(ROOT / "sdk/python/pyproject.toml"), re.M)
    if not m or not m2:
        raise SystemExit("python SDK version not found")
    if m.group(1) != m2.group(1):
        raise SystemExit(f"python __version__ {m.group(1)} != pyproject {m2.group(1)}")
    return m.group(1)


def pom_version() -> str:
    m = re.search(r"<name>parsedock-client</name>\s*<version>([^<]+)</version>",
                  read(ROOT / "sdk/java-generated/pom.xml"))
    if not m:
        raise SystemExit("java pom version not found")
    return m.group(1)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=str(ROOT / "runtime/bin/docparse"))
    args = ap.parse_args()

    versions = {"openapi": openapi_version(), "python": python_version(), "java": pom_version()}
    out = subprocess.run([args.bin, "--version"], capture_output=True, text=True, check=True).stdout
    try:
        versions["binary"] = json.loads(out)["version"]
    except (json.JSONDecodeError, KeyError):
        versions["binary"] = out.strip().split()[-1]

    bad = {k: v for k, v in versions.items() if v != versions["openapi"]}
    print(json.dumps(versions, indent=2, sort_keys=True))
    if bad:
        print(f"version drift: {bad} != openapi {versions['openapi']}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
