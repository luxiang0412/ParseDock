# LICENSE REVIEW — Phase 0

> Reviewed 2026-10-30 against the exact artifacts that were installed and
> executed (see `docs/ENVIRONMENT.md`). This is an engineering review, not legal
> advice: items marked **LEGAL SIGN-OFF** must be confirmed by counsel before
> production distribution.

Method: read the repository `LICENSE`, the installed package metadata
(`*.dist-info/METADATA`, `License-Expression`), the model cards on Hugging Face,
and the shipped `THIRD_PARTY_LICENSES.md` inside the release tarball. Version
SPDX tags alone were not trusted.

## 1. Summary

| Component | Version | Code license | Model / data license | Verdict for Phase 0 PoC | Verdict for commercial deployment |
| --- | --- | --- | --- | --- | --- |
| xberg (CLI + core) | 1.3.10 | MIT (`Copyright (c) 2025-2026 Kreuzberg, Inc.`) | none bundled | OK | **LEGAL SIGN-OFF** — release tarball ships `libx265.so.199` (GPL-2.0-or-later) |
| docling-rs | 2.53.3 | MIT | `models-v1` bundle: layout/table = Docling models (Apache-2.0 / CDLA-Permissive), OCR = PP-OCR (Apache-2.0) | OK | OK with attribution notes |
| Docling (Python) | 2.145.0 | MIT (`License-Expression: MIT`) | layout `docling-layout-heron` Apache-2.0; TableFormer CDLA-Permissive-1.0 | OK | OK |
| docling-serve | 1.38.2 | MIT | same as Docling | OK | OK |
| MinerU | 4.1.2 | `LicenseRef-MinerU-Open-Source-License` = Apache-2.0 **+ additional terms** | flash tier uses `PP-DocLayoutV2` + `PP-OCRv6` (Apache-2.0); MinerU VLM weights (`MinerU2.5-2509-1.2B`) are **AGPL-3.0** | OK for `flash` | **LEGAL SIGN-OFF + attribution obligation**, and never use the AGPL VLM weights |
| Apache Tika | 4.2.0 (`latest-full`) | Apache-2.0 | n/a | OK | OK (image also contains permissive deps; see §5) |
| tesseract / tessdata | 5.5.0 / `eng`, `chi_sim` | Apache-2.0 | Apache-2.0 | OK | OK |
| Go dependencies | see `go.sum` | `modernc.org/sqlite` (BSD-3), `golang.org/x/text` (BSD-3) | n/a | OK | OK |
| Python stack | torch 2.15.0+cu132, transformers 5.22.0, etc. | BSD-style / Apache-2.0 | n/a | OK | review bundled CUDA EULA for redistribution |

## 2. xberg — MIT core, GPL helper library in the tarball

* Repository license: **MIT** (`github.com/xberg-io/xberg`, verified 2026-10-30).
* Released CLI tarball `xberg-cli-x86_64-unknown-linux-gnu.tar.gz` (v1.3.10)
  bundles shared libraries: `libheif.so.1`, `libde265.so.0`, `libx265.so.199`,
  `libaom.so.3`, `libnuma.so.1`.
* The shipped `THIRD_PARTY_LICENSES.md` states:

  | Library | Purpose | License |
  | --- | --- | --- |
  | libheif | HEIF/HEIC/AVIF container | LGPL |
  | libde265 | HEVC decode | LGPL-3.0-or-later |
  | libx265 | HEVC **encode** | **GPL-2.0-or-later** |
  | libaom | AV1 | BSD-style |
  | libnuma | NUMA | LGPL-2.1+ |

**Engineering consequence:** ParseDock only *decodes* documents; HEVC encoding
is never used. Two mitigations exist and one is mandatory before shipping the
binary to customers:

1. Build/obtain an xberg artifact compiled without `libx265` (upstream supports
   a musl build and a source build without the HEVC encoder), **or**
2. ship the GPL library as a separate, replaceable `.so` together with the
   corresponding-source offer that GPL-2.0 requires, **or**
3. call xberg as a *separate process* over a documented interface (which is what
   ParseDock does) and evaluate whether the aggregation boundary is acceptable.

The process boundary helps but does not by itself settle distribution
obligations for the bundled GPL library — hence **LEGAL SIGN-OFF** before any
customer-facing binary includes the upstream tarball.

Evidence: `runtime/engines/xberg-cli/THIRD_PARTY_LICENSES.md`, lines 12–80.
The `strings` check on the shipped `.so` files found no embedded license banner,
so the tarball documentation is the only authoritative statement.

## 3. MinerU — Apache-2.0 with extra commercial and attribution terms

`pip show mineru` reports `License-Expression: LicenseRef-MinerU-Open-Source-License`.
The full text (`mineru-4.1.2.dist-info/licenses/LICENSE.md`, 3 301 bytes) is
Apache-2.0 plus these **additional** terms:

1. **Commercial threshold** — commercial use is allowed without a separate
   license *unless* the user and its affiliates exceed **100 M MAU** or
   **USD 20 M monthly revenue** on a consolidated basis; beyond that a separate
   commercial license is required.
2. **Online-service attribution** — if MinerU is used to provide an online
   service to third parties, the product UI or public documentation must
   clearly state that MinerU is used.
3. **Automatic termination** if (1) is triggered without a license or (2) is
   violated.

This is *not* plain Apache-2.0 and it is *not* AGPL for the code. For an internal
document-parsing service the thresholds are irrelevant; for a product offered to
external customers, the attribution clause is an active obligation that must be
implemented (e.g. "parsing powered by MinerU" in docs/about screen).

**Model weights — the important distinction.** Phase 0 verified what the local
`flash` tier actually downloads:

```
~/.mineru/models/MinerU-4_models_onnx/Layout/PP-DocLayoutV2/inference.onnx      (Apache-2.0)
~/.mineru/models/MinerU-4_models_onnx/OCR/paddleocr/ch_PP-OCRv6_*_infer.onnx     (Apache-2.0)
```

The MinerU VLM weights used by the managed/remote tiers
(`opendatalab/MinerU2.5-2509-1.2B`) are **AGPL-3.0**. ParseDock only ever runs
`flash` locally and never calls `mineru.net`, so AGPL weights are out of scope —
**but this must be enforced in code and deployment**, not just documented. The
adapter hard-codes `--tier flash` and refuses other tiers; `k8s`/compose egress
policy should block `mineru.net`.

Also note MinerU 4.1 ships **telemetry enabled by default** (prerelease). The
PoC disables it (`mineru telemetry disable`) and the GPU image does the same at
build time.

## 4. Docling family — MIT code, permissive models

* `docling` 2.145.0: `License-Expression: MIT`.
* `docling-serve` 1.38.2: MIT (`Classifier: License :: OSI Approved :: MIT License`).
* `docling.rs` 2.53.3: repository MIT.
* Model weights observed:
  * layout: `docling-project/docling-layout-heron` → `apache-2.0`
  * table structure (TableFormer): Docling models, CDLA-Permissive-1.0
  * OCR: PaddleOCR PP-OCRv6 (Apache-2.0), RapidOCR Apache-2.0
* docling-rs's `models-v1` release is a conversion of the above into ONNX; the
  release page carries no separate restrictive terms. Record the tag
  (`models-v1`) and file hashes in the deployment manifest.

No copyleft obligation reaches ParseDock's code when Docling is invoked as a
subprocess or over HTTP. Attribution is still recommended in `NOTICE`.

## 5. Apache Tika image

* Tika itself: Apache-2.0.
* `apache/tika:latest-full` (digest
  `sha256:4774eaac44f86cdf648ae7b5a367016eaf6f9052adec31e5cff07cff1a0c6e24`,
  Tika 4.2.0) adds OCR and media parsers; the image's own `NOTICE`/`LICENSE`
  inventory must be shipped with the image. `latest-full` is a **moving tag**:
  Phase 0 pins the digest; production must pin a digest too.

## 6. Container and SDK licenses

* Base images: `golang:1.24-bookworm` (build only), `debian:trixie-slim`
  (runtime, DFSG-free), `nvidia/cuda:13.2.0-runtime-ubuntu24.04` (optional GPU
  worker — its CUDA EULA governs redistribution).
* OpenAPI Generator output: Apache-2.0 for the generator; generated clients
  carry whatever license the project assigns (ParseDock code is Apache-2.0 in
  this repo's default configuration; adjust if the organisation requires
  another).

## 6b. Distribution gate (PR-00 / F11)

`scripts/license_audit.sh` (wired as `make license-audit`) inspects the built
images and writes `build/sbom/<image>.json` with the dpkg inventory, bundled
shared libraries, GPL/LGPL findings and the notes that need human action. It
**fails** when an image contains a GPL library unless an approved exception is
recorded (`APPROVED_GPL=1 APPROVED_GPL_REASON=...`).

Current result on this host:

| Image | GPL | LGPL | Gate |
| --- | --- | --- | --- |
| `parsedock:0.1.0` | none | none | PASS |
| `parsedock-worker-cpu:0.1.0` | `libx265.so.199` | `libde265`, `libheif`, `libnuma` | **BLOCK** |

The gate is engineering evidence, not a legal decision: **L1 below stays open and
BLOCKED for external distribution.** The LGPL libraries must remain replaceable
(dynamic `.so`, not statically linked) and their notices must ship with the image.

## 7. Open items to close before production

| # | Item | Owner | Blocking? |
| --- | --- | --- | --- |
| L1 | Decide libx265 handling in the xberg distribution (rebuild vs. source offer vs. exclude) | legal + build | Yes, for shipping the CLI bundle |
| L2 | Implement MinerU attribution text if the service is offered to third parties | product | Conditional |
| L3 | Verify no `mineru.net` egress and no non-`flash` tier at runtime | platform | Yes |
| L4 | Pin Tika by digest, ship its NOTICE inventory | platform | Yes |
| L5 | Record model hashes (`docling models-v1`, MinerU `PP-*`) in the release manifest | platform | Recommended |
| L6 | Review CUDA redistribution terms if the GPU worker image is shipped | legal | Conditional |
