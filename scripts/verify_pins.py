#!/usr/bin/env python3
"""Verify that build inputs are pinned and consistent (PR-01, requirement 4).

`images.lock.json` is the single source of truth. This script fails when a
Dockerfile, workflow, go.mod or requirements file drifts from it, or when a
Dockerfile uses a floating `FROM image:tag` without a digest.

    python3 scripts/verify_pins.py [--root DIR]

Exit code 0 = consistent, 1 = drift (each finding printed).
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REQUIRED_LOCK_KEYS = {"go", "images", "engines"}
FLOATING_FROM = re.compile(r"^\s*FROM\s+(?P<image>[^\s]+)(?:\s+AS\s+\S+)?\s*$", re.IGNORECASE)


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError:
        return ""


def verify(root: Path) -> list[str]:
    problems: list[str] = []

    lock_path = root / "images.lock.json"
    try:
        lock = json.loads(read(lock_path))
    except json.JSONDecodeError as e:
        return [f"{lock_path}: invalid JSON ({e})"]

    missing = REQUIRED_LOCK_KEYS - set(lock)
    if missing:
        problems.append(f"images.lock.json: missing keys {sorted(missing)}")
        return problems
    images = lock.get("images") or {}
    engines = lock.get("engines") or {}
    if not images or not engines:
        problems.append("images.lock.json: images/engines must not be empty")
        return problems

    for name, ref in images.items():
        if "@sha256:" not in ref:
            problems.append(f"images.lock.json: image {name} is not digest-pinned: {ref}")

    # go.mod toolchain must match the lock (setup-go reads go.mod).
    gomod = read(root / "go.mod")
    m = re.search(r"^go\s+(\S+)", gomod, re.MULTILINE)
    if not m:
        problems.append("go.mod: no go directive")
    elif m.group(1) != lock["go"]:
        problems.append(f"go.mod go {m.group(1)} != images.lock.json go {lock['go']}")

    # Dockerfiles: every FROM must be digest-pinned, and the locked images must
    # appear where we expect them.
    dockerfiles = sorted((root / "docker").glob("Dockerfile*"))
    if not dockerfiles:
        problems.append("docker/: no Dockerfile found")
    for df in dockerfiles:
        text = read(df)
        for line in text.splitlines():
            fm = FLOATING_FROM.match(line)
            if not fm:
                continue
            image = fm.group("image")
            if image.startswith("${") or image == "parsedock:0.1.0":
                continue  # build-arg/derived stage bases are documented as such
            if "@sha256:" not in image:
                problems.append(f"{df.relative_to(root)}: FROM {image} is not digest-pinned")

    dockerfile = read(root / "docker" / "Dockerfile")
    for key in ("go_builder", "runtime"):
        ref = images.get(key)
        if ref and ref not in dockerfile:
            problems.append(f"docker/Dockerfile does not use images.lock.json image {key} ({ref})")
    if "GOTOOLCHAIN=local" not in dockerfile:
        problems.append("docker/Dockerfile must set GOTOOLCHAIN=local (no toolchain downloads)")
    if "-buildvcs=false" not in dockerfile:
        problems.append("docker/Dockerfile must build with -buildvcs=false for reproducibility")

    gpu = read(root / "docker" / "Dockerfile.worker-gpu")
    cuda = images.get("cuda_runtime")
    if gpu and cuda and cuda not in gpu:
        problems.append("docker/Dockerfile.worker-gpu does not use the locked CUDA base")

    artifacts = (engines.get("artifacts") or {})
    for name in ("xberg_cli", "docling_rs"):
        art = artifacts.get(name) or {}
        sha = art.get("sha256", "")
        if not re.fullmatch(r"[0-9a-f]{64}", sha):
            problems.append(f"images.lock.json: engines.artifacts.{name}.sha256 missing or invalid")
        elif sha not in read(root / "docker" / "Dockerfile.engines"):
            problems.append(f"docker/Dockerfile.engines does not pin the {name} sha256")
        else:
            installer = read(root / "scripts" / "ci-install-engines.sh")
            # The installer must verify the artefact, either with the literal hash
            # or by reading the locked value (preferred: one source of truth).
            if sha not in installer and f"engines.artifacts.{name}.sha256" not in installer:
                problems.append(f"scripts/ci-install-engines.sh does not verify the {name} sha256")
    models = artifacts.get("docling_rs_models_script") or {}
    commit = models.get("commit", "")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        problems.append("images.lock.json: engines.artifacts.docling_rs_models_script.commit missing")
    elif f"docling.rs/{commit}/scripts/install/download_dependencies.sh" not in read(root / "docker" / "Dockerfile.engines"):
        problems.append("docker/Dockerfile.engines does not pin the models download script to the locked commit")

    engines_df = read(root / "docker" / "Dockerfile.engines")
    for arg, key in (("XBERG_VERSION", "xberg"), ("DOCLING_RS_VERSION", "docling_rs"),
                     ("DOCLING_RS_MODELS_TAG", "docling_rs_models")):
        m = re.search(rf"^ARG\s+{arg}=(\S+)", engines_df, re.MULTILINE)
        if not m:
            problems.append(f"docker/Dockerfile.engines: missing ARG {arg}")
        elif m.group(1) != engines[key]:
            problems.append(f"docker/Dockerfile.engines {arg}={m.group(1)} != lock {engines[key]}")
    if engines.get("tesseract_langs"):
        m = re.search(r'^ARG\s+TESSERACT_LANGS="([^"]+)"', engines_df, re.MULTILINE)
        if not m:
            problems.append("docker/Dockerfile.engines: missing ARG TESSERACT_LANGS")
        elif m.group(1) != engines["tesseract_langs"]:
            problems.append("docker/Dockerfile.engines TESSERACT_LANGS != lock")

    # Workflows must use the locked tool images (no floating tags in CI).
    for wf in sorted((root / ".github" / "workflows").glob("*.yml")):
        text = read(wf)
        for m in re.finditer(r"^\s*([A-Z_]+_IMAGE):\s*(\S+)\s*$", text, re.MULTILINE):
            name, value = m.group(1), m.group(2)
            if "@sha256:" not in value:
                problems.append(f"{wf.relative_to(root)}: {name} is not digest-pinned: {value}")
            if value.startswith("openapitools/") and value != images.get("openapi_generator"):
                problems.append(f"{wf.relative_to(root)}: {name} does not match images.lock.json")
            if value.startswith("apache/tika") and value != images.get("tika"):
                problems.append(f"{wf.relative_to(root)}: {name} does not match images.lock.json")

    # Tool versions in scripts must match the lock.
    tools = lock.get("tools") or {}
    installer = read(root / "scripts" / "install-actionlint.sh")
    if tools.get("actionlint"):
        m = re.search(r'^VERSION="([^"]+)"', installer, re.MULTILINE)
        if not m:
            problems.append("scripts/install-actionlint.sh: no VERSION pin")
        elif m.group(1) != tools["actionlint"]:
            problems.append(f"install-actionlint.sh VERSION={m.group(1)} != lock {tools['actionlint']}")
        if not re.search(r'^SHA256="[0-9a-f]{64}"', installer, re.MULTILINE):
            problems.append("scripts/install-actionlint.sh: download is not checksum-pinned")
    for key, script, checksum in (("golangci_lint", "install-golangci-lint.sh", r'^SHA256="[0-9a-f]{64}"'),
                                  ("govulncheck", "install-govulncheck.sh", r'^H1="h1:[A-Za-z0-9+/=]{44}"')):
        installer = read(root / "scripts" / script)
        if not tools.get(key):
            problems.append(f"images.lock.json: tools.{key} is missing")
            continue
        m = re.search(r'^VERSION="([^"]+)"', installer, re.MULTILINE)
        if not m:
            problems.append(f"scripts/{script}: no VERSION pin")
        elif m.group(1) != tools[key]:
            problems.append(f"{script} VERSION={m.group(1)} != lock {tools[key]}")
        if not re.search(checksum, installer, re.MULTILINE):
            problems.append(f"scripts/{script}: download is not checksum-pinned")

    # Python CI dependencies must be version-pinned.
    req = read(root / lock.get("python", {}).get("ci_requirements", "requirements-ci.txt"))
    if not req.strip():
        problems.append("requirements-ci.txt is missing or empty")
    for line in req.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "==" not in line:
            problems.append(f"requirements-ci.txt: unpinned requirement {line!r}")
    if tools.get("ruff"):
        m = re.search(r"^ruff==(\S+)", req, re.MULTILINE)
        if not m:
            problems.append("requirements-ci.txt: no ruff== pin")
        elif m.group(1) != tools["ruff"]:
            problems.append(f"requirements-ci.txt ruff=={m.group(1)} != lock {tools['ruff']}")

    # Actions must be pinned by commit SHA (no floating vX tags), in workflows and in the
    # local composite actions they call.
    for wf in sorted([*(root / ".github" / "workflows").glob("*.yml"),
                      *(root / ".github" / "actions").glob("*/action.yml")]):
        text = read(wf)
        for m in re.finditer(r"uses:\s*(\S+)@(\S+)", text):
            action, ref = m.group(1), m.group(2)
            if action.startswith("./"):
                continue
            if not re.fullmatch(r"[0-9a-f]{40}", ref):
                problems.append(f"{wf.relative_to(root)}: action {action}@{ref} is not pinned to a commit SHA")

    return problems


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--root", default=str(Path(__file__).resolve().parents[1]))
    args = ap.parse_args()
    root = Path(args.root).resolve()
    problems = verify(root)
    if problems:
        print(f"PIN VERIFICATION FAILED ({len(problems)} problem(s)):")
        for p in problems:
            print(f"  - {p}")
        return 1
    print("pins OK: go.mod, Dockerfiles, workflows and images.lock.json are consistent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
