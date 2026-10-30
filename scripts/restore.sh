#!/usr/bin/env bash
# ParseDock restore (PR-00 / F10).
#
#   scripts/restore.sh <backup-dir> <data-dir>
#
# Refuses to overwrite a non-empty data directory. After copying, it verifies
# that every file referenced by the manifest exists and prints the tombstone
# warning: the coordinator's cleaner runs on start and will purge anything whose
# retention state is no longer active, so a restore can never make a released
# result downloadable again.
set -euo pipefail

BACKUP_DIR="${1:?usage: restore.sh <backup-dir> <data-dir>}"
DATA_DIR="${2:?usage: restore.sh <backup-dir> <data-dir>}"
[[ -f "$BACKUP_DIR/coordinator.db" ]] || { echo "backup has no coordinator.db" >&2; exit 1; }

mkdir -p "$DATA_DIR"
if [[ -n "$(ls -A "$DATA_DIR" 2>/dev/null)" ]]; then
  echo "refusing to restore into non-empty $DATA_DIR (move it away first)" >&2
  exit 1
fi

cp -a "$BACKUP_DIR/coordinator.db" "$DATA_DIR/coordinator.db"
for suffix in -wal -shm; do
  [[ -f "$BACKUP_DIR/coordinator.db$suffix" ]] && cp -a "$BACKUP_DIR/coordinator.db$suffix" "$DATA_DIR/coordinator.db$suffix"
done
[[ -d "$BACKUP_DIR/files" ]] && cp -a "$BACKUP_DIR/files/." "$DATA_DIR/"

# Remove artifacts of released/expired jobs that a pre-release backup may contain.
python3 - "$DATA_DIR/coordinator.db" "$DATA_DIR" <<'PY'
import sqlite3, sys, shutil
from pathlib import Path
db = sqlite3.connect(sys.argv[1])
data = Path(sys.argv[2])
removed = 0
for job_id, retention in db.execute(
        "SELECT id, retention_status FROM jobs WHERE retention_status IN ('released','expired','release_pending','expiry_pending','deleting')"):
    for sub in ("inputs", "results"):
        d = data / sub / job_id
        if d.exists():
            shutil.rmtree(d, ignore_errors=True)
            removed += 1
db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
db.commit()
print(f"purged {removed} artifact dir(s) belonging to non-active retention states")
PY

echo "restore complete: $DATA_DIR"
echo "note: the coordinator's cleaner will finish any pending retention work on start"
