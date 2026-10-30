#!/usr/bin/env bash
# Build the control-plane and CPU worker images with matching tags (PR-01).
#
#   scripts/build-images.sh [suffix] [--no-cache]
#
# suffix defaults to 0.1.0 (what deploy/compose.cpu.yaml expects). The worker
# image is built FROM the control-plane image via BASE_IMAGE, so this script is
# the only place that has to know the relationship.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUFFIX="${1:-0.1.0}"
NOCACHE=""
if [[ "${2:-}" == "--no-cache" || "${1:-}" == "--no-cache" ]]; then NOCACHE="--no-cache"; fi
if [[ "${1:-}" == "--no-cache" ]]; then SUFFIX="0.1.0"; fi

CONTROL="parsedock:${SUFFIX}"
WORKER="parsedock-worker-cpu:${SUFFIX}"

echo "building ${CONTROL}"
docker build ${NOCACHE} -f "$ROOT/docker/Dockerfile" -t "$CONTROL" "$ROOT"

echo "building ${WORKER} (base ${CONTROL})"
docker build ${NOCACHE} \
  --build-arg "BASE_IMAGE=${CONTROL}" \
  -f "$ROOT/docker/Dockerfile.engines" \
  -t "$WORKER" "$ROOT"

echo "built:"
docker image inspect "$CONTROL" --format '  {{.RepoTags}} id={{.Id}}'
docker image inspect "$WORKER" --format '  {{.RepoTags}} id={{.Id}}'
