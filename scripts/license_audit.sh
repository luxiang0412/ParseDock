#!/usr/bin/env bash
# ParseDock licence / SBOM gate (PR-00 / F11).
#
#   scripts/license_audit.sh [image ...]
#
# For every image it writes build/sbom/<image>.json containing:
#   * the base image reference and the image digest,
#   * the installed dpkg package inventory,
#   * bundled shared libraries and the GPL/LGPL markers found in the image,
#   * licence notes that require human action (MinerU attribution, CUDA EULA).
#
# Exit code:
#   0  no blocking finding (or APPROVED_GPL=1 with APPROVED_GPL_REASON set)
#   1  a GPL library is present in an image that is meant for distribution
#   2  docker is unavailable / image missing (PARTIAL, not a pass)
#
# This gate is engineering evidence only. The legal decision on libx265 is L1 in
# docs/LICENSE-REVIEW.md and is NOT made here.
set -uo pipefail

IMAGES=("$@")
if [[ ${#IMAGES[@]} -eq 0 ]]; then
  IMAGES=(parsedock:0.1.0 parsedock-worker-cpu:0.1.0)
fi
OUT_DIR="${SBOM_DIR:-build/sbom}"
mkdir -p "$OUT_DIR"
status=0

for image in "${IMAGES[@]}"; do
  safe_name="${image//[:\/]/_}"
  json="$OUT_DIR/$safe_name.json"
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "SKIP  $image (not built)" >&2
    printf '{"image":"%s","status":"missing"}\n' "$image" > "$json"
    status=2
    continue
  fi

  digest=$(docker image inspect "$image" --format '{{index .RepoDigests 0}}' 2>/dev/null || echo "")
  image_id=$(docker image inspect "$image" --format '{{.Id}}')
  base=$(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.base.name"}}' 2>/dev/null || true)

  tmp=$(mktemp)
  docker run --rm --entrypoint sh "$image" -c '
    echo "##PACKAGES"; (dpkg -l 2>/dev/null || true) | awk "NR>5 {print \$2\"|\"\$3}"
    echo "##LIBRARIES"; find / -xdev \( -name "*.so" -o -name "*.so.*" \) 2>/dev/null | grep -v "^/proc" | sort
    echo "##GPL_FILES"; find / -xdev -name "libx265*" 2>/dev/null | sort
    echo "##LGPL_FILES"; find / -xdev \( -name "libde265*" -o -name "libheif*" -o -name "libnuma*" \) 2>/dev/null | sort
    echo "##XBERG_THIRD_PARTY"; if [ -f /opt/xberg/THIRD_PARTY_LICENSES.md ]; then grep -c "" /opt/xberg/THIRD_PARTY_LICENSES.md; else echo 0; fi
    echo "##MIXERU"; command -v mineru >/dev/null 2>&1 && echo present || echo absent
  ' > "$tmp" 2>/dev/null || true

  python3 - "$image" "$image_id" "$digest" "$base" "$tmp" "$json" <<'PY'
import json, sys
image, image_id, digest, base, tmp, out = sys.argv[1:7]
packages, libraries, gpl, lgpl, xberg_lines, mineru = [], [], [], [], 0, "absent"
section = None
for line in open(tmp, encoding="utf-8", errors="replace"):
    line = line.rstrip("\n")
    if line.startswith("##"):
        section = line[2:]
        continue
    if not line:
        continue
    if section == "PACKAGES":
        parts = line.split("|")
        if len(parts) == 2:
            packages.append({"name": parts[0], "version": parts[1]})
    elif section == "LIBRARIES":
        libraries.append(line)
    elif section == "GPL_FILES":
        gpl.append(line)
    elif section == "LGPL_FILES":
        lgpl.append(line)
    elif section == "XBERG_THIRD_PARTY":
        try:
            xberg_lines = int(line)
        except ValueError:
            xberg_lines = 0
    elif section == "MINERU":
        mineru = line

notes = []
if lgpl:
    notes.append("LGPL libraries present (" + ", ".join(sorted({p.split('/')[-1] for p in lgpl})) +
                 "): dynamic linking is acceptable, but the .so must stay replaceable and the "
                 "licence text must ship with the image.")
if any("libx265" in p for p in gpl):
    notes.append("libx265 (GPL-2.0-or-later) is bundled: external distribution is "
                 "BLOCKED on legal decision L1 (rebuild without HEVC encode, or ship "
                 "the GPL source offer). Process isolation does not resolve this.")
if xberg_lines:
    notes.append(f"xberg THIRD_PARTY_LICENSES.md present ({xberg_lines} lines)")
if mineru == "present":
    notes.append("MinerU present: Apache-2.0 + additional terms; an online service must "
                 "state that MinerU is used. Only the local flash tier may be enabled.")
if "nvidia" in (base or "") or "cuda" in image:
    notes.append("CUDA runtime redistribution terms require review before shipping the GPU image.")
notes.append("Model weights: docling models-v1 (permissive) and MinerU flash PP-OCR/PP-DocLayout "
             "(Apache-2.0); AGPL-3.0 MinerU VLM weights are out of scope.")

doc = {
    "image": image, "image_id": image_id, "repo_digest": digest, "base": base,
    "packages": packages, "packages_count": len(packages),
    "bundled_libraries": len(libraries),
    "gpl_libraries": gpl,
    "lgpl_libraries": lgpl,
    "xberg_third_party_doc_lines": xberg_lines,
    "mineru": mineru,
    "notes": notes,
}
json.dump(doc, open(out, "w"), indent=2)
print(f"wrote {out}: {len(packages)} packages, {len(gpl)} GPL, {len(lgpl)} LGPL library file(s)")
PY
  rm -f "$tmp"

  gpl_count=$(python3 -c "import json,sys;print(len(json.load(open('$json'))['gpl_libraries']))")
  if [[ "$gpl_count" -gt 0 ]]; then
    if [[ "${APPROVED_GPL:-0}" == "1" && -n "${APPROVED_GPL_REASON:-}" ]]; then
      echo "ALLOW $image: $gpl_count GPL library file(s) covered by approved reason: $APPROVED_GPL_REASON"
    else
      echo "BLOCK $image: $gpl_count GPL library file(s) found (see $json)." >&2
      echo "      Resolve L1 in docs/LICENSE-REVIEW.md, or set APPROVED_GPL=1 APPROVED_GPL_REASON='…' for an approved internal build." >&2
      status=1
    fi
  else
    echo "PASS  $image: no GPL library found"
  fi
done

exit "$status"
