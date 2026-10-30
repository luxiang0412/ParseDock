#!/usr/bin/env bash
# Reproducible-build check for the control-plane binary (PR-01, requirement 4).
#
#   scripts/repro-check.sh [image]
#
# 1. Builds the binary twice into different directories with the pinned toolchain
#    and compares sha256: identical output means the build does not depend on the
#    checkout path, VCS metadata or a build id.
# 2. When an image is given (default parsedock:ci), compares the binary inside the
#    image with the local one. A match proves the container ships exactly the
#    artefact this check verified (same Go version, same flags).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${1:-parsedock:ci}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

build() {
  local out="$1"
  ( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local \
      go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o "$out" ./cmd/docparse )
}

echo "go version: $(go version)"
build "$TMP/a"
sleep 1
build "$TMP/b"
HA=$(sha256sum "$TMP/a" | awk '{print $1}')
HB=$(sha256sum "$TMP/b" | awk '{print $1}')
echo "local build 1: $HA"
echo "local build 2: $HB"

status=0
if [[ "$HA" != "$HB" ]]; then
  echo "REPRO FAILED: two local builds differ" >&2
  status=1
else
  echo "REPRO OK: local builds are byte-identical"
fi

if docker image inspect "$IMAGE" >/dev/null 2>&1; then
  IMG_HASH="$(docker run --rm --entrypoint sha256sum "$IMAGE" /usr/local/bin/docparse | awk '{print $1}')"
  echo "image binary : $IMG_HASH"
  if [[ "$IMG_HASH" != "$HA" ]]; then
    echo "REPRO FAILED: image binary differs from the local build" >&2
    echo "  (check Go version in the image: docker run --rm --entrypoint docparse $IMAGE --check)" >&2
    status=1
  else
    echo "REPRO OK: image ships the verified binary"
  fi
else
  echo "NOTE: image $IMAGE not present; skipped image/binary comparison"
fi

mkdir -p "$ROOT/build/ci"
{
  echo "go_version=$(go version | awk '{print $3}')"
  echo "local_build_1=$HA"
  echo "local_build_2=$HB"
  echo "image=$IMAGE"
  echo "image_binary=$IMG_HASH"
  echo "status=$([[ $status -eq 0 ]] && echo pass || echo fail)"
} > "$ROOT/build/ci/repro.txt"
cat "$ROOT/build/ci/repro.txt"
exit "$status"
