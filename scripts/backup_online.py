#!/usr/bin/env python3
"""Online backup of a running ParseDock coordinator (PR-12).

Unlike ``scripts/backup.sh`` this does **not** stop the coordinator:

* the database is copied with SQLite's backup API, which takes a consistent snapshot of a
  live WAL database (no torn pages, no missing WAL);
* the file tree is copied afterwards, driven by the snapshot, so every file copied belongs
  to a job that existed when the snapshot was taken;
* only jobs whose results are still servable are copied. A released result must never come
  back from a backup — an ACK is a deletion request.

    scripts/backup_online.py --data-dir /data --out /backups/2026-11-01 [--verify]

``--verify`` re-reads the copy (PRAGMA integrity_check, per-artifact SHA-256) and prints a
JSON report with timings, which doubles as the RTO input for the restore drill.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import shutil
import sqlite3
import sys
import time
from pathlib import Path

# Retention states whose files are still servable. Everything else is cleanup-in-progress
# or already gone.
SERVABLE = ("active",)


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def copy_tree(src: Path, dst: Path, jobs: set[str]) -> tuple[int, int]:
    files = bytes_copied = 0
    for sub in ("inputs", "results"):
        sroot = src / sub
        if not sroot.is_dir():
            continue
        for job_dir in sorted(sroot.iterdir()):
            if job_dir.name not in jobs:
                continue
            target = dst / sub / job_dir.name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copytree(job_dir, target, symlinks=False, dirs_exist_ok=True)
            for f in target.rglob("*"):
                if f.is_file():
                    files += 1
                    bytes_copied += f.stat().st_size
    return files, bytes_copied


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--data-dir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--verify", action="store_true")
    ap.add_argument("--skip-files", action="store_true",
                    help="metadata only (for a quick RPO test on a large tree)")
    args = ap.parse_args()

    data = Path(args.data_dir)
    out = Path(args.out)
    src_db = data / "coordinator.db"
    if not src_db.exists():
        print(f"no coordinator.db in {data}", file=sys.stderr)
        return 1
    out.mkdir(parents=True, exist_ok=True)
    started = time.time()

    # 1. Consistent database snapshot (online).
    t0 = time.time()
    src = sqlite3.connect(f"file:{src_db}?mode=ro", uri=True)
    dst = sqlite3.connect(out / "coordinator.db")
    with dst:
        src.backup(dst)
    src.close()
    dst.close()
    db_seconds = time.time() - t0

    # 2. Which jobs still have servable files?
    conn = sqlite3.connect(out / "coordinator.db")
    jobs = {row[0] for row in conn.execute(
        "SELECT id FROM jobs WHERE retention_status IN (%s)" % ",".join("?" * len(SERVABLE)),
        SERVABLE)}
    artifacts = conn.execute(
        "SELECT job_id, kind, path, size, sha256 FROM artifacts").fetchall()
    conn.close()

    files = bytes_copied = 0
    if not args.skip_files:
        files, bytes_copied = copy_tree(data, out / "files", jobs)

    report = {
        "backup": str(out),
        "db_seconds": round(db_seconds, 3),
        "total_seconds": round(time.time() - started, 3),
        "jobs_servable": len(jobs),
        "files": files,
        "bytes": bytes_copied,
        "verify": None,
    }

    if args.verify:
        t1 = time.time()
        problems = []
        check = sqlite3.connect(out / "coordinator.db")
        ok = check.execute("PRAGMA integrity_check").fetchone()[0]
        if ok != "ok":
            problems.append(f"integrity_check: {ok}")
        for job_id, kind, rel, size, want in artifacts:
            # The JSON manifest records paths under the source data dir; resolve the copy.
            p = out / "files" / "results" / job_id / Path(rel).name
            if kind in ("markdown", "text", "json") and not p.exists():
                p = out / "files" / "results" / job_id / kind
            if not p.exists():
                if job_id in jobs:
                    problems.append(f"missing artifact {job_id}/{kind}")
                continue
            got = sha256(p)
            if want and got != want:
                problems.append(f"sha mismatch {job_id}/{kind}: {got[:12]} != {want[:12]}")
            if size and p.stat().st_size != size:
                problems.append(f"size mismatch {job_id}/{kind}")
        check.close()
        report["verify"] = {"seconds": round(time.time() - t1, 3),
                            "problems": problems, "ok": not problems}

    (out / "backup-report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(json.dumps(report, indent=2, sort_keys=True))
    if args.verify and not report["verify"]["ok"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
