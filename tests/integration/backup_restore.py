#!/usr/bin/env python3
"""PR00-13: a backup/restore must never resurrect an ACKed or expired result.

Flow:
  1. run a coordinator, create two jobs;
  2. ACK (and let the cleaner release) the first, keep the second active;
  3. stop the coordinator and run scripts/backup.sh;
  4. assert the backup contains only the active job's artifacts;
  5. restore into a fresh directory, start a coordinator there;
  6. assert the active job is downloadable and the released one is not.

    python tests/integration/backup_restore.py
"""
from __future__ import annotations

import json
import shutil
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from acceptance import FIX, ROOT, Stack, http_request  # noqa: E402

RAW = ROOT / "benchmark" / "results" / "raw"
SCRATCH = ROOT / "runtime" / "pr00-backup"


def main() -> int:
    detail: dict = {}
    backup_dir = SCRATCH / "backup"
    restore_dir = SCRATCH / "restored"
    shutil.rmtree(SCRATCH, ignore_errors=True)
    SCRATCH.mkdir(parents=True)

    stack = Stack("pr00-backup", engines="mock", api_count=1, worker_count=1,
                  worker_env={"PARSEDOCK_MOCK_DELAY_MS": "100"},
                  coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s",
                              "--tombstone-ttl", "30s"])
    stack.start()
    try:
        # job A: released before the backup
        a = stack.create_job(FIX / "txt_plain.txt").json()["job_id"]
        fa = stack.wait_status(a, {"completed"}, timeout=60)
        assert stack.get_result(a).status == 200
        assert stack.ack(a, fa["result_version"]).status == 202
        stack.wait_retention(a, {"released"}, timeout=40)
        # job B: still active
        b = stack.create_job(FIX / "csv_utf8.csv").json()["job_id"]
        stack.wait_status(b, {"completed"}, timeout=60)
        assert stack.get_result(b).status == 200
        detail["jobs"] = {"released": a, "active": b}
    finally:
        stack.stop()

    data_dir = stack.data_dir
    detail["files_before_backup"] = {
        "released": sorted(p.relative_to(data_dir).as_posix() for p in (data_dir / "results" / a).rglob("*") if p.is_file()),
        "active": sorted(p.relative_to(data_dir).as_posix() for p in (data_dir / "results" / b).rglob("*") if p.is_file()),
    }

    # 3. offline backup
    cp = subprocess.run([str(ROOT / "scripts" / "backup.sh"), str(data_dir), str(backup_dir)],
                        capture_output=True, text=True)
    assert cp.returncode == 0, cp.stderr
    detail["backup_stdout"] = cp.stdout.strip()
    manifest = json.loads((backup_dir / "MANIFEST.json").read_text())
    detail["manifest_active_jobs"] = manifest["active_jobs"]
    assert manifest["active_jobs"] == 1, manifest

    backup_results = {p.name for p in (backup_dir / "files" / "results").iterdir()} if (backup_dir / "files" / "results").exists() else set()
    detail["backup_result_dirs"] = sorted(backup_results)
    assert b in backup_results, "active job's result missing from the backup"
    assert a not in backup_results, "released job's result was backed up (would resurrect on restore)"

    # 5. restore into a fresh directory
    cp = subprocess.run([str(ROOT / "scripts" / "restore.sh"), str(backup_dir), str(restore_dir)],
                        capture_output=True, text=True)
    assert cp.returncode == 0, cp.stderr
    detail["restore_stdout"] = cp.stdout.strip()

    restored = Stack("pr00-restore", engines="mock", api_count=1, worker_count=1,
                     data_dir_override=restore_dir,
                     coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s"])
    restored.start(wipe=False)
    try:
        active = restored.get_result(b)
        released = restored.get_result(a)
        detail["restored_active_download"] = active.status
        detail["restored_released_download"] = released.status
        assert active.status == 200, f"active result lost in restore: {active.status}"
        assert released.status in (410, 404), f"released result resurrected: {released.status}"
        assert not (restore_dir / "results" / a).exists(), "released artifacts exist after restore"
        mapi = restored.get_result(b, "json")
        assert mapi.status == 200
        detail["restored_active_version"] = restored.get_job(b).json().get("result_version")
    finally:
        restored.stop()

    # 7. the tombstone row may still exist (410) or be gone (404); both are correct
    RAW.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    out = RAW / f"pr00-backup-{stamp}.json"
    out.write_text(json.dumps({"finished_at": datetime.now(timezone.utc).isoformat(),
                               "status": "PASS", "detail": detail}, indent=2), encoding="utf-8")
    print("[PR00-13] PASS  backup/restore does not resurrect released results")
    print(json.dumps(detail, indent=2)[:1200])
    print(f"raw: {out.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
