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
import datetime
import hashlib
import json
import shutil
import sqlite3
import sys
import threading
import time
import uuid
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


def copy_tree(src: Path, dst: Path, jobs: set[str],
              progress=None) -> tuple[int, int]:
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
            if progress:
                progress()
    return files, bytes_copied


class Leases:
    """Cleanup protection for the snapshot's jobs, renewed while the copy runs.

    The cleaner honours download_leases, so an ACK or expiry during the backup cannot delete
    a directory the snapshot still claims. Leases are renewed at a third of their lifetime so
    a copy that takes longer than one lease window stays protected.
    """

    def __init__(self, db_path: Path, seconds: int):
        self.db_path = db_path
        self.seconds = seconds
        self.ids: list[str] = []
        # A background thread renews the leases, so a single large directory cannot outlive
        # them: renewal must not depend on finishing a copytree() call.
        self.conn = sqlite3.connect(db_path, check_same_thread=False)
        self.lock = threading.Lock()
        self.stop_event = threading.Event()
        self.thread = threading.Thread(target=self._loop, daemon=True)
        self.thread.start()
        self.last = time.time()
        self.renew_errors: list[str] = []

    def _loop(self) -> None:
        interval = max(1, self.seconds // 3)
        while not self.stop_event.wait(interval):
            self.renew()

    def pin(self, jobs: set[str]) -> list[str]:
        now = datetime.datetime.now(datetime.timezone.utc)
        exp = now + datetime.timedelta(seconds=self.seconds)
        stamp = lambda d: d.isoformat(timespec="microseconds").replace("+00:00", "Z")
        unpinnable = []
        with self.conn:
            for job_id in sorted(jobs):
                lease = f"backup-{uuid.uuid4().hex}"
                cur = self.conn.execute(
                    "INSERT OR IGNORE INTO download_leases"
                    "(lease_id, job_id, created_at, expires_at, last_renewed_at)"
                    " SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM jobs WHERE id = ?)",
                    (lease, job_id, stamp(now), stamp(exp), stamp(now), job_id))
                if cur.rowcount == 1:
                    self.ids.append(lease)
                else:
                    unpinnable.append(job_id)
        return unpinnable

    def progress(self) -> None:
        self.renew()

    def renew(self) -> None:
        now = datetime.datetime.now(datetime.timezone.utc)
        exp = now + datetime.timedelta(seconds=self.seconds)
        stamp = lambda d: d.isoformat(timespec="microseconds").replace("+00:00", "Z")
        with self.lock:
            ids = list(self.ids)
            if not ids:
                return
            try:
                with self.conn:
                    self.conn.executemany(
                        "UPDATE download_leases SET expires_at = ?, last_renewed_at = ? WHERE lease_id = ?",
                        [(stamp(exp), stamp(now), lease) for lease in ids])
            except sqlite3.Error as exc:
                self.renew_errors.append(str(exc))
        self.last = time.time()

    def release(self) -> None:
        self.stop_event.set()
        self.thread.join(timeout=5)
        with self.lock:
            if self.ids:
                try:
                    with self.conn:
                        self.conn.executemany("DELETE FROM download_leases WHERE lease_id = ?",
                                              [(l,) for l in self.ids])
                except sqlite3.Error as exc:
                    self.renew_errors.append(str(exc))
            self.conn.close()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--data-dir", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--verify", dest="verify", action="store_true", default=True,
                    help="verify the copy (default)")
    ap.add_argument("--no-verify", dest="verify", action="store_false")
    ap.add_argument("--skip-files", action="store_true",
                    help="metadata only (for a quick RPO test on a large tree)")
    ap.add_argument("--lease-seconds", type=int, default=900,
                    help="how long the backup pins active jobs against cleanup")
    args = ap.parse_args()

    data = Path(args.data_dir)
    out = Path(args.out)
    src_db = data / "coordinator.db"
    if not src_db.exists():
        print(f"no coordinator.db in {data}", file=sys.stderr)
        return 1
    # A reused directory would keep artifacts of jobs that are no longer servable (the copy
    # only overlays the current set), so the destination must be empty.
    if out.exists() and any(out.iterdir()):
        print(f"refusing to reuse non-empty {out} (released artifacts would survive)", file=sys.stderr)
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

    # 3. Pin every job in the snapshot before copying files. Pinning deliberately does not
    # re-check retention: a job that was ACKed between snapshot and pin must still be
    # protected (its snapshot row says active), and a job whose row no longer exists cannot
    # be pinned — that is reported as an incomplete backup rather than silently skipped.
    leases, db_seconds_lock, unpinnable = None, 0.0, []
    if jobs and not args.skip_files:
        t_lock = time.time()
        leases = Leases(src_db, args.lease_seconds)
        unpinnable = leases.pin(jobs)
        db_seconds_lock = time.time() - t_lock
        # A pin can succeed while the files are already gone: the cleaner may have released
        # and deleted them between the snapshot and the INSERT (only the tombstone row
        # remains). Detect that directly instead of trusting the pin.
        jobs_with_artifacts = {a[0] for a in artifacts}
        vanished = [j for j in sorted(jobs)
                    if j in jobs_with_artifacts and not (data / "results" / j).exists()]
        if unpinnable:
            vanished.extend(j for j in unpinnable
                            if not (data / "results" / j).exists()
                            and not (data / "inputs" / j).exists())
            jobs = jobs - set(unpinnable)
        if vanished:
            leases.release()
            print(json.dumps({"error": "incomplete backup: snapshot results were deleted "
                                       "before they could be pinned", "jobs": sorted(set(vanished))},
                             indent=2), file=sys.stderr)
            return 1

    files = bytes_copied = 0
    try:
        if not args.skip_files:
            files, bytes_copied = copy_tree(data, out / "files", jobs,
                                            progress=leases.progress if leases else None)
    finally:
        if leases:
            leases.renew()          # final renewal covers the last copy window
            leases.release()

    report = {
        "backup": str(out),
        "db_seconds": round(db_seconds, 3),
        "lease_seconds": round(db_seconds_lock, 3),
        "unpinnable_jobs": unpinnable,
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
