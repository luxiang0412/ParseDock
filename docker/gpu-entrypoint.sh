#!/bin/sh
# Start MinerU's local daemon (required by its CLI in 4.1), wait for it, then
# exec the ParseDock worker so signals reach the worker directly.
set -eu
mineru telemetry disable >/dev/null 2>&1 || true
mineru server start >/dev/null 2>&1 || true
i=0
while [ "$i" -lt 60 ]; do
  if mineru server status >/dev/null 2>&1; then break; fi
  i=$((i + 1)); sleep 1
done
exec /usr/local/bin/docparse "$@"
