#!/usr/bin/env bash
# ParseDock offline backup (PR-00 / F10).
#
#   scripts/backup.sh <data-dir> <backup-dir>
#
# What is backed up:
#   * coordinator.db (+ WAL/SHM) — job metadata, leases, events
#   * inputs/ and results/ of jobs whose artifacts are still *servable*
#     (retention_status = active) — nothing else, on purpose:
#       - released/expired artifacts must not come back from a backup (ACK is a
#         deletion request; resurrecting it would violate the client's intent);
#       - 'deleting'/'release_pending' jobs are in-flight cleanup and are skipped.
#
# Preconditions:
#   * the coordinator is STOPPED (offline backup). An online backup is possible
#     with `sqlite3 coordinator.db ".backup out.db"`, but the file tree must be
#     snapshotted in the same quiescent window. PR-06 owns the online variant.
#
# The restore script re-runs the cleaner after copying, so a stale row can never
# point at a missing file.
set -euo pipefail

DATA_DIR="${1:?usage: backup.sh <data-dir> <backup-dir>}"
BACKUP_DIR="${2:?usage: backup.sh <data-dir> <backup-dir>}"
DB="$DATA_DIR/coordinator.db"

# Refuse a non-empty destination: copying only the current job set into an old tree would
# leave artifacts that are no longer servable (and an old WAL beside the new database).
if [[ -d "$BACKUP_DIR" && -n "$(ls -A "$BACKUP_DIR" 2>/dev/null)" ]]; then
  echo "refusing to reuse non-empty $BACKUP_DIR" >&2
  exit 1
fi

[[ -f "$DB" ]] || { echo "no coordinator.db in $DATA_DIR" >&2; exit 1; }
mkdir -p "$BACKUP_DIR/files"

# 1. Metadata (offline copy, WAL included).
cp -a "$DB" "$BACKUP_DIR/coordinator.db"
for suffix in -wal -shm; do
  [[ -f "$DB$suffix" ]] && cp -a "$DB$suffix" "$BACKUP_DIR/coordinator.db$suffix"
done

# 2. Only active artifacts. The list comes from the DB itself.
python3 - "$DB" > "$BACKUP_DIR/active-jobs.txt" <<'PY'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("PRAGMA query_only=ON")
for (job_id,) in db.execute(
        "SELECT id FROM jobs WHERE retention_status = 'active' ORDER BY id"):
    print(job_id)
PY

count=0
while read -r job; do
  [[ -z "$job" ]] && continue
  if [[ -d "$DATA_DIR/inputs/$job" ]]; then
    mkdir -p "$BACKUP_DIR/files/inputs"
    cp -a "$DATA_DIR/inputs/$job" "$BACKUP_DIR/files/inputs/"
  fi
  if [[ -d "$DATA_DIR/results/$job" ]]; then
    mkdir -p "$BACKUP_DIR/files/results"
    cp -a "$DATA_DIR/results/$job" "$BACKUP_DIR/files/results/"
  fi
  count=$((count + 1))
done < "$BACKUP_DIR/active-jobs.txt"

cat > "$BACKUP_DIR/MANIFEST.json" <<JSON
{
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "source_data_dir": "$DATA_DIR",
  "active_jobs": $count,
  "contents": ["coordinator.db", "files/inputs (active only)", "files/results (active only)"],
  "excluded_on_purpose": ["released artifacts", "expired artifacts", "release_pending/deleting jobs"]
}
JSON
echo "backup complete: $BACKUP_DIR ($count active jobs)"
