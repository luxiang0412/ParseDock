#!/usr/bin/env python3
"""Verify that a ParseDock backup can actually serve its jobs.

A backup is not "done" because the files were copied: every active job must still have the
input it needs to resume, and every completed job must have the manifest and artifacts its
result is served from. This check is shared by the online backup (`--verify`) and by
`scripts/restore.sh`, so a restore can never announce success over an unusable copy.

    scripts/verify_backup.py --dir /backups/2026-11-01 [--json]
"""
from __future__ import annotations

import argparse
import hashlib
import json
import sqlite3
import sys
from pathlib import Path

# The three representations the API serves; a result without one of them is not restorable.
SERVED = ("result.md", "result.txt", "result.json")


def manifest_version(files: list[dict]) -> str:
    """Mirror storage.manifestVersion: sha256 over "name:size:sha\n" per file."""
    h = hashlib.sha256()
    for f in files:
        h.update(f"{f['name']}:{f['size']}:{f['sha256']}\n".encode())
    return "sha256:" + h.hexdigest()

# Jobs whose files must survive a restore. A permanently failed job keeps
# retention_status=active but the coordinator already deleted its files, so it is checked
# for metadata only.
NEEDS_FILES = ("queued", "running", "completed")


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def verify(backup: Path) -> dict:
    db = backup / "coordinator.db"
    # A backup keeps the database beside a `files/` tree; a restored data directory has the
    # tree at its root. Accept both so restore.sh can verify what it just copied.
    files = backup / "files" if (backup / "files").is_dir() else backup
    problems: list[str] = []
    if not db.exists():
        return {"ok": False, "problems": ["no coordinator.db"], "checked": {}}

    check = sqlite3.connect(f"file:{db}?mode=ro", uri=True)
    ok = check.execute("PRAGMA integrity_check").fetchone()[0]
    if ok != "ok":
        problems.append(f"integrity_check: {ok}")

    jobs = check.execute(
        "SELECT id, processing_status, retention_status, input_size, input_sha256,"
        " result_version FROM jobs WHERE retention_status = 'active'").fetchall()
    # The jobs table keys on `id`; artifacts use `job_id`.
    manifests = {row[0]: row[1] for row in
                 check.execute("SELECT id, result_version FROM jobs"
                               " WHERE retention_status = 'active' AND processing_status = 'completed'")}
    artifacts = check.execute("SELECT job_id, kind, size, sha256 FROM artifacts").fetchall()
    check.close()

    counts = {"jobs": len(jobs), "inputs": 0, "manifests": 0, "artifacts": 0}
    with_files = {j[0] for j in jobs if j[1] in NEEDS_FILES}

    for job_id, status, _retention, input_size, input_sha, _version in jobs:
        if job_id not in with_files:
            continue
        p = files / "inputs" / job_id / "input.bin"
        if not p.exists():
            problems.append(f"missing input {job_id}")
            continue
        counts["inputs"] += 1
        if input_size and p.stat().st_size != input_size:
            problems.append(f"input size mismatch {job_id}")
        if input_sha and sha256(p) != input_sha:
            problems.append(f"input hash mismatch {job_id}")

    resolved = {}
    for job_id, version in manifests.items():
        gen = files / "results" / job_id / (version or "").removeprefix("sha256:")
        mp = gen / "manifest.json"
        if not mp.exists():
            # Backups (and installations) from before content-addressed generations keep the
            # manifest directly under results/<job>/.
            legacy = files / "results" / job_id / "manifest.json"
            if legacy.exists():
                gen, mp = files / "results" / job_id, legacy
        resolved[job_id] = gen
        if not mp.exists():
            problems.append(f"missing manifest {job_id}")
            continue
        counts["manifests"] += 1
        try:
            man = json.loads(mp.read_text())
        except (OSError, json.JSONDecodeError) as exc:
            problems.append(f"unreadable manifest {job_id}: {exc}")
            continue
        if version and man.get("result_version") != version:
            problems.append(f"manifest version mismatch {job_id}")
        if not man.get("files"):
            problems.append(f"manifest lists no files {job_id}")
            continue
        names = {f.get("name") for f in man["files"]}
        for want in SERVED:
            if want not in names:
                problems.append(f"manifest is missing {want} for {job_id}")
        # Recompute the version the coordinator would advertise: a manifest whose files were
        # altered (or whose version was never consistent) must not pass.
        try:
            computed = manifest_version(man["files"])
        except (KeyError, TypeError) as exc:
            problems.append(f"manifest entries are malformed for {job_id}: {exc}")
            computed = ""
        if computed and man.get("result_version") and computed != man["result_version"]:
            problems.append(f"manifest version does not match its files for {job_id}")
        if computed and not man.get("result_version"):
            problems.append(f"manifest has no result_version for {job_id}")
        for entry in man["files"]:
            fp = gen / entry["name"]
            if not fp.exists():
                problems.append(f"missing artifact {job_id}/{entry['name']}")
                continue
            counts["artifacts"] += 1
            if fp.stat().st_size != entry["size"]:
                problems.append(f"artifact size mismatch {job_id}/{entry['name']}")

    # Artifact rows are authoritative too: a manifest that omits a served artifact would
    # let a restore fail on download rather than here.
    # Every served representation must have both a manifest entry and an artifact row: a
    # result is served through the manifest, and storage accounting is driven by the row.
    served_by_job: dict[str, set[str]] = {}
    for job_id, kind, size, want in artifacts:
        served_by_job.setdefault(job_id, set()).add(kind)
    for job_id in manifests:
        have = served_by_job.get(job_id, set())
        for want in SERVED:
            if want not in have:
                problems.append(f"no artifact row for {want} of {job_id}")

    for job_id, kind, size, want in artifacts:
        if job_id not in with_files:
            continue
        version = manifests.get(job_id)
        if not version:
            continue
        fp = resolved.get(job_id, files / "results" / job_id / version.removeprefix("sha256:")) / kind
        if not fp.exists():
            problems.append(f"missing artifact row {job_id}/{kind}")
        elif want and sha256(fp) != want:
            problems.append(f"artifact hash mismatch {job_id}/{kind}")

    return {"ok": not problems, "problems": problems, "checked": counts}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()
    report = verify(Path(args.dir))
    if args.json:
        print(json.dumps(report, indent=2, sort_keys=True))
    else:
        print(f"checked {report['checked']}; problems: {len(report['problems'])}")
        for p in report["problems"]:
            print(f"  - {p}")
    return 0 if report["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
