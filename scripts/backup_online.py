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
        self.thread: threading.Thread | None = None
        self.last = time.time()
        self.renew_errors: list[str] = []

    def _loop(self) -> None:
        interval = max(1, self.seconds // 3)
        while not self.stop_event.wait(interval):
            self.renew()

    def start(self) -> None:
        # Started after pinning: the thread and the pin would otherwise share one SQLite
        # connection and could commit each other's transaction halfway through.
        self.thread = threading.Thread(target=self._loop, daemon=True)
        self.thread.start()

    def pin(self, jobs: set[str]) -> list[str]:
        now = datetime.datetime.now(datetime.timezone.utc)
        exp = now + datetime.timedelta(seconds=self.seconds)
        stamp = lambda d: d.isoformat(timespec="microseconds").replace("+00:00", "Z")
        unpinnable = []
        with self.lock, self.conn:
            for job_id in sorted(jobs):
                lease = f"backup-{uuid.uuid4().hex}"
                # Only a job that is still 'active' can be protected: once cleanup has
                # marked it 'deleting' it deletes files without re-checking leases, and a
                # released/expired job has nothing to protect. A job that changed state
                # between the snapshot and this INSERT is reported, and the caller retries.
                cur = self.conn.execute(
                    "INSERT OR IGNORE INTO download_leases"
                    "(lease_id, job_id, created_at, expires_at, last_renewed_at)"
                    " SELECT ?, ?, ?, ?, ? WHERE EXISTS"
                    " (SELECT 1 FROM jobs WHERE id = ? AND retention_status = 'active')",
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
        if self.thread is not None:
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
    # Only states that actually keep their input need it to exist: a permanently failed
    # (or cancelled) job has its files deleted by the coordinator but stays 'active' until
    # the failed-retention period elapses, so demanding an input would reject a valid state.
    conn_inputs = conn.execute(
        "SELECT id, input_size, input_sha256 FROM jobs WHERE retention_status = 'active'"
        " AND processing_status IN ('queued','running','completed')").fetchall()
    completed = conn.execute(
        "SELECT id, result_version, result_manifest_path FROM jobs"
        " WHERE retention_status = 'active' AND processing_status = 'completed'").fetchall()
    conn_state = conn.execute(
        "SELECT id, processing_status FROM jobs WHERE retention_status = 'active'").fetchall()
    input_sha = {row[0]: row[2] for row in conn_inputs}
    conn_inputs = [(row[0], row[1]) for row in conn_inputs]
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
        if not unpinnable:
            leases.start()
        db_seconds_lock = time.time() - t_lock
        # A job that left 'active' between the snapshot and the pin (ACK, TTL, cancel) cannot
        # be protected: cleanup may delete its files without re-checking leases. Rather than
        # producing an incomplete backup that looks fine, abort and let the operator retry —
        # the window is milliseconds and the next attempt takes a fresh snapshot.
        # Only jobs that must still have files can "vanish": a failed/cancelled job keeps
        # artifact metadata but the coordinator deleted its files on purpose.
        jobs_with_artifacts = {a[0] for a in artifacts}
        needs_files = {row[0] for row in conn_state if row[1] in ("queued", "running", "completed")}
        vanished = [j for j in sorted(jobs)
                    if j in jobs_with_artifacts and j in needs_files
                    and not (data / "results" / j).exists()]
        if unpinnable or vanished:
            leases.release()
            shutil.rmtree(out, ignore_errors=True)
            print(json.dumps({
                "error": "snapshot and pinning disagreed: jobs changed retention state during "
                         "the backup. The partial destination was removed; retry the same "
                         "command (it takes a fresh snapshot).",
                "unpinnable": sorted(set(unpinnable)),
                "missing_files": sorted(set(vanished)),
            }, indent=2), file=sys.stderr)
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
        # Inputs are what a queued/running job needs to resume; artifacts exist only for
        # completed jobs, so checking just artifacts would bless an unrestorable backup.
        needs_files = {row[0] for row in conn_state if row[1] in ("queued", "running", "completed")}
        for job_id, input_size in conn_inputs:
            p = out / "files" / "inputs" / job_id / "input.bin"
            if not p.exists():
                if job_id in jobs:
                    problems.append(f"missing input {job_id}")
                continue
            if input_size and p.stat().st_size != input_size:
                problems.append(f"input size mismatch {job_id}: {p.stat().st_size} != {input_size}")
            if sha256(p) != input_sha[job_id]:
                problems.append(f"input hash mismatch {job_id}")
        # A completed result is served through its manifest; a missing or malformed
        # manifest makes the restored job undownloadable even when every artifact file
        # matches SQLite.
        for job_id, version, _manifest_path in completed:
            mp = out / "files" / "results" / job_id / "manifest.json"
            if not mp.exists():
                if job_id in jobs:
                    problems.append(f"missing manifest {job_id}")
                continue
            try:
                man = json.loads(mp.read_text())
            except (OSError, json.JSONDecodeError) as exc:
                problems.append(f"unreadable manifest {job_id}: {exc}")
                continue
            # The manifest stores the version as result_version (storage.Manifest).
            if version and man.get("result_version") != version:
                problems.append(f"manifest version mismatch {job_id}: "
                                f"{man.get('result_version')} != {version}")
            if not man.get("files"):
                problems.append(f"manifest lists no files {job_id}")

        for job_id, kind, rel, size, want in artifacts:
            # The JSON manifest records paths under the source data dir; resolve the copy.
            p = out / "files" / "results" / job_id / Path(rel).name
            if kind in ("markdown", "text", "json") and not p.exists():
                p = out / "files" / "results" / job_id / kind
            if not p.exists():
                # Artifact rows survive a permanent failure; the files do not.
                if job_id in jobs and job_id in needs_files:
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
