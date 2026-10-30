#!/bin/sh
# GPU worker entrypoint.
#
# 1. When started as root (a mounted volume is owned by root, so the image-time
#    chown cannot help), prepare the work tree and drop privileges to 10001.
# 2. Start MinerU's local daemon (required by its CLI in 4.1) and wait for it.
# 3. exec the ParseDock worker so signals reach the worker directly.
set -eu

WORK_ROOT="${PARSEDOCK_WORK_ROOT:-/work}"

# MinerU/HuggingFace write to $HOME (and a token file), which is /root when the
# container starts as root: point both at the writable work volume.
export HOME="$WORK_ROOT/mineru-home"
export HF_HOME="$WORK_ROOT/mineru-home/hf"
export XDG_CACHE_HOME="$WORK_ROOT/mineru-home/.cache"

if [ "$(id -u)" = "0" ]; then
  mkdir -p "$WORK_ROOT/mineru-home" "$WORK_ROOT/scratch" "$HF_HOME"
  chown -R 10001:10001 "$WORK_ROOT" 2>/dev/null || true
  exec setpriv --reuid=10001 --regid=10001 --clear-groups "$0" "$@"
fi

mineru telemetry disable >/dev/null 2>&1 || true
mineru server start >/dev/null 2>&1 || true
i=0
while [ "$i" -lt 120 ]; do
  if mineru server status >/dev/null 2>&1; then break; fi
  i=$((i + 1)); sleep 1
done

# Scratch must never be the volume root: the worker's scrubber removes attempt
# directories older than an hour and would delete the model cache with them.
case " $* " in
  *" --work-dir "*|*" --work-dir="*) ;;
  *) set -- "$@" --work-dir "$WORK_ROOT/scratch" ;;
esac

exec /usr/local/bin/docparse "$@"
