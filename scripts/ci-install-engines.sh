#!/usr/bin/env bash
# Install the real parsing engines for CI acceptance runs (PR-01).
#
#   scripts/ci-install-engines.sh [xberg,docling-rs]
#
# Versions come from images.lock.json so CI and the worker image agree. The
# script is idempotent and only used by the nightly / engine-change workflows and
# by `make acceptance-real` when the engines are not present yet.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WANT="${1:-xberg,docling-rs}"
ENGINES="$ROOT/runtime/engines"
mkdir -p "$ENGINES"

read_lock() { python3 - "$ROOT/images.lock.json" "$1" <<'PY'
import json, sys
lock = json.load(open(sys.argv[1]))
node = lock
for part in sys.argv[2].split("."):
    node = node[part]
print(node)
PY
}

XBERG_VERSION="$(read_lock engines.xberg)"
DOCLING_RS_VERSION="$(read_lock engines.docling_rs)"

has_xberg=0; has_docling=0
case ",$WANT," in *,xberg,*) has_xberg=1;; esac
case ",$WANT," in *,docling-rs,*) has_docling=1;; esac

if [[ "$has_xberg" == 1 ]]; then
  if [[ ! -x "$ENGINES/xberg-cli/xberg" ]]; then
    echo "installing xberg $XBERG_VERSION"
    tmp="$(mktemp -d)"
    curl -fsSL -o "$tmp/xberg.tgz" \
      "https://github.com/xberg-io/xberg/releases/download/v${XBERG_VERSION}/xberg-cli-x86_64-unknown-linux-gnu.tar.gz"
    rm -rf "$ENGINES/xberg-cli"
    mkdir -p "$ENGINES/xberg-cli"
    tar -xzf "$tmp/xberg.tgz" -C "$ENGINES/xberg-cli" --strip-components=1
    rm -rf "$tmp"
  fi
  LD_LIBRARY_PATH="$ENGINES/xberg-cli" "$ENGINES/xberg-cli/xberg" --version
fi

if [[ "$has_docling" == 1 ]]; then
  if [[ ! -x "$ENGINES/docling-rs/docling-rs" ]]; then
    echo "installing docling-rs $DOCLING_RS_VERSION"
    tmp="$(mktemp -d)"
    curl -fsSL -o "$tmp/docling-rs.tgz" \
      "https://github.com/docling-project/docling.rs/releases/download/v${DOCLING_RS_VERSION}/docling-rs-v${DOCLING_RS_VERSION}-x86_64-unknown-linux-gnu.tar.gz"
    mkdir -p "$ENGINES/docling-rs"
    tar -xzf "$tmp/docling-rs.tgz" -C "$ENGINES/docling-rs"
    chmod +x "$ENGINES/docling-rs/docling-rs"
    rm -rf "$tmp"
  fi
  if [[ ! -f "$ENGINES/docling-rs/.models/layout_heron.onnx" ]]; then
    echo "downloading docling-rs models ($(read_lock engines.docling_rs_models))"
    ( cd "$ENGINES/docling-rs" && \
      curl -fsSL https://raw.githubusercontent.com/docling-project/docling.rs/master/scripts/install/download_dependencies.sh \
        | bash -s -- --no-asr --no-chunk )
  fi
  "$ENGINES/docling-rs/docling-rs" --version
fi

echo "engines ready: $WANT"
