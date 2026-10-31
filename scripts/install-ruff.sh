#!/usr/bin/env bash
# Install the pinned ruff (Python lint in CI and locally) into runtime/ci-venv.
#
#   scripts/install-ruff.sh
#
# The version comes from requirements-ci.txt (`ruff==X.Y.Z`, checked by
# scripts/verify_pins.py against images.lock.json). A private venv keeps it working on
# PEP-668 managed hosts and never touches the system Python.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PIN="$(grep -E '^ruff==' "$ROOT/requirements-ci.txt" || true)"
if [[ -z "$PIN" ]]; then
  echo "requirements-ci.txt has no ruff== pin" >&2
  exit 1
fi
VERSION="${PIN#ruff==}"
VENV="$ROOT/runtime/ci-venv"
BIN="$VENV/bin/ruff"

if [[ -x "$BIN" ]] && "$BIN" --version 2>/dev/null | grep -qx "ruff $VERSION"; then
  echo "ruff $VERSION already installed"
  exit 0
fi

[[ -x "$VENV/bin/python" ]] || python3 -m venv "$VENV"
"$VENV/bin/python" -m pip install --disable-pip-version-check -q "$PIN"
"$BIN" --version
