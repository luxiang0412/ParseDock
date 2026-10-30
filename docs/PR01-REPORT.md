# PR-01 REPORT — CI baseline and reproducible builds

> Completed 2026-10-30. Repository: `github.com/luxiang0412/ParseDock` (private).
> Scope: PR-01 only (no PR-02…PR-12 work). This report cites **real GitHub Actions
> runs**, not local simulations.

## 1. Summary

| PR-01 requirement | Result |
| --- | --- |
| GitHub Actions for vet/test/build, OpenAPI, contract, SDK drift, PR-00 regressions | **DONE** — 4 workflows, 6 blocking PR jobs |
| Mock lifecycle acceptance on every PR; real engines nightly + on engine changes | **DONE** — `ci/lifecycle`, `nightly/real-engines`, `engine-changes/real-engines` |
| Blocking checks; contract failure prevents merge | **PARTIAL** — gate implemented and proven red on failure (negative-control run `41041186706`); platform refuses branch protection on a private free-plan repo (§6b) |
| Reproducible builds with pinned Go, dependencies and image digests | **DONE** — `images.lock.json` + `verify_pins.py` + `repro-check.sh` (byte-identical) |
| Full logs, acceptance JSON, build summary, failure diagnostics as artifacts | **DONE** — `build/ci/` per job (logs, step timings, JSON, SBOM, digest) |
| CI runtime ≤ 8 min for critical PR checks, with caching | **DONE** — 3 m 53 s – 3 m 58 s measured (lifecycle is the critical path) |
| Licence audit distinguishes control-plane PASS from worker BLOCK | **DONE** — `licence-classification.txt` + `release-gate` enforce mode |
| No API/ACK/TTL/worker/IR behaviour changes | **DONE** — no runtime code changed; tests unchanged and green |
| Actually trigger CI (not just local runs) | **DONE** — 7 CI runs, 2 nightly runs and 4 engine-changes runs on GitHub-hosted runners, including 1 failing nightly and a negative-control PR (§4, §6, §6b) |

**Verdict: PR-01 is complete except one platform-blocked item** — enforced merge
blocking (branch protection) requires a paid plan or a public repository. **GO for
PR-02**; the enforcement gap must be closed by the repository owner before the
public API/SDK are frozen (it is a one-command change once the plan allows it).

## 2. Changes and commits

Commits on `main` (oldest first). Each is reviewable on its own.

| Commit | Content |
| --- | --- |
| `01de1a9` | `build(sdk)`: deterministic SDK generation (timestamp hidden, target wiped, generator pinned, `.gitattributes` for LF/binary) — makes the SDK drift check possible |
| `1839b71` | `ci:` workflows, `images.lock.json`, `verify_pins.py`, `ci-local.sh`, `container-smoke.sh`, `build-images` groundwork, Makefile targets |
| `e9c43b2` | `ci:` pinned actions bumped to Node 24 releases (checkout v7, setup-go v7, upload-artifact v7, cache v6) |
| `85b2764` | `fix(ci)`: worker image `FROM ${BASE_IMAGE}` + plain `docker build` (buildx could not see the local base image — found by the failing nightly run) |
| `1200ad4` | `fix(ci)`: artifacts restricted to the current run (marker files); `classify_licence.py` + persisted classification |
| `docs` commit | `docs/CI.md`, `docs/PR01-REPORT.md`, README + IMPLEMENTATION-PLAN updates |

File map:

```
.github/workflows/ci.yml               blocking PR pipeline
.github/workflows/engine-changes.yml   path-classified engine/docker verification
.github/workflows/nightly.yml          real engines + container smoke + SBOM
.github/workflows/release-gate.yml     blocking licence gate on tags
images.lock.json                       pinned Go, images, engines, tools
requirements-ci.txt                    PyYAML==6.0.2
scripts/ci-local.sh                    all CI phases (single implementation)
scripts/build-images.sh                control plane + worker with BASE_IMAGE
scripts/container-smoke.sh             real-engine end-to-end through compose
scripts/repro-check.sh                 byte-identical build proof
scripts/verify_pins.py                 drift gate for every pinned input
scripts/classify_licence.py            control PASS / worker BLOCK classification
scripts/install-actionlint.sh          checksum-pinned workflow linter
scripts/license_audit.sh               + AUDIT_MODE=report|enforce
scripts/ci-install-engines.sh          pinned xberg/docling-rs for CI
Makefile                               ci, ci-phase, verify-pins, lint-workflows, container-smoke
docs/CI.md                             CI map, artifacts, licence policy, limitations
```

No file under `internal/`, `cmd/`, `api/` or `sdk/python/parsedock_sdk/` changed
behaviour; `go test ./...`, the T03–T16 acceptance suite and the PR-00 runtime
contract are unchanged and pass.

## 3. Workflow structure

`ci-local.sh <phase>` holds every command; workflows only schedule and collect.
A PR therefore cannot pass CI while failing a local `make ci` (and vice versa).

| Job (PR) | Phase | Timeout | What it proves |
| --- | --- | --- | --- |
| `static` | `static` | 10 m | pins, gofmt, vet, build, actionlint, `go mod tidy` drift |
| `unit` | `unit` | 10 m | Go tests (42 functions) + scoring rubric (21 tests) |
| `contract` | `contract` | 15 m | OpenAPI 3.1 (public+internal), router/SDK drift, **SDK regeneration drift** |
| `lifecycle` | `lifecycle` | 25 m | T03–T16, PR-00 runtime contract (10), backup/restore |
| `image` | `image` | 20 m | image build, `--check`, reproducible binary, licence report |
| `ci-summary` | – | 5 m | aggregates results, writes the step summary, fails the PR if any job failed |

`engine-changes.yml` runs on every PR, classifies changed paths in a `detect` job
and skips the heavy jobs when nothing engine/docker-related changed (so it reports
`success` and is safe to require). `nightly.yml` and `release-gate.yml` are
described in `docs/CI.md`.

## 4. Real CI runs (evidence)

| Run | Trigger | SHA | Result | Wall time |
| --- | --- | --- | --- | --- |
| [41036163447](https://github.com/luxiang0412/ParseDock/actions/runs/41036163447) | push `main` | `1839b71` | **success** | 3 m 58 s |
| [41036753219](https://github.com/luxiang0412/ParseDock/actions/runs/41036753219) | push `main` (Node 24 action pins) | `e9c43b2` | **success** | 3 m 57 s |
| [41038125488](https://github.com/luxiang0412/ParseDock/actions/runs/41038125488) | push `main` (base-image/plain-builder fix) | `85b2764` | **success** | 3 m 53 s |
| [41038813735](https://github.com/luxiang0412/ParseDock/actions/runs/41038813735) | push `main` (artifact scoping) | `1200ad4` | **success** | 3 m 45 s |
| [41037246798](https://github.com/luxiang0412/ParseDock/actions/runs/41037246798) | `workflow_dispatch` nightly | `e9c43b2` | **failure** (real bug, §6) | 3 m 22 s |
| [41038145488](https://github.com/luxiang0412/ParseDock/actions/runs/41038145488) | `workflow_dispatch` nightly (after fix) | `85b2764` | **success** | 1 m 56 s |
| [41039343974](https://github.com/luxiang0412/ParseDock/actions/runs/41039343974) | **pull_request #1** | `e0b7154` | **success** (6/6 checks) | 3 m 56 s |
| [41041186706](https://github.com/luxiang0412/ParseDock/actions/runs/41041186706) | pull_request #2 (negative control) | `546f198` | **failure, as intended** (contract + ci-summary) | 3 m 58 s |

Job timings of the PR run `41039343974` (`static` 1 m 47 s, `unit` 0 m 54 s,
`contract` 0 m 39 s, `image` 2 m 07 s, `lifecycle` 3 m 43 s, `ci-summary` 0 m 05 s;
total 3 m 56 s) match the main-branch run `41038125488`:

| Job | Started | Finished | Duration |
| --- | --- | --- | --- |
| `contract` | 10:05:14 | 10:06:00 | 0 m 46 s |
| `unit` | 10:05:14 | 10:06:22 | 1 m 08 s |
| `static` | 10:05:14 | 10:06:40 | 1 m 26 s |
| `image` | 10:05:14 | 10:07:14 | 2 m 00 s |
| `lifecycle` | 10:05:14 | 10:08:57 | **3 m 43 s** |
| `ci-summary` | 10:09:00 | 10:09:02 | 0 m 02 s |
| total | 10:05:10 | 10:09:03 | **3 m 53 s** |

Nightly `41038145488`: `real-engines` 1 m 34 s, `container-smoke` 1 m 51 s, total
1 m 56 s. The real-engine job installed xberg 1.3.10 + docling-rs 2.53.3 (590 MB of
models), ran T01/T02/T11 (all PASS, engine `xberg` for the PDF), ran the quality
subset and regenerated `scores.json`. The container job built both images, ran the
smoke test (`fast → xberg`, `accurate → docling-rs`, download 200 / ACK 202 /
post-ACK 410) and produced the SBOM.

Artifacts (run `41038125488`): `ci-lifecycle` 91 KB (acceptance + PR-00 JSON and
stack logs), `ci-image` 6.6 KB (digest, repro hashes, SBOM, `--check` JSON),
`ci-contract` 1.5 KB, `ci-unit` 1.3 KB, `ci-static` 1.3 KB. Nightly:
`nightly-real-engines` 27 KB, `nightly-container` 15 KB (smoke JSON + SBOM +
classification).

## 5. Local reproduction (`make ci`, dev host)

| Step | Duration |
| --- | --- |
| static (pins, fmt, vet, build, actionlint, tidy drift) | 1.6 s |
| unit (go test + 21 scoring tests) | 1.7 s |
| contract (2 spec validations + 29 checks + SDK regeneration) | 4.7 s |
| lifecycle (T03–T16 + PR-00 + backup/restore) | 146.5 s |
| image (build, check, digest, repro, licence report) | 3.4 s |
| **all** | **156.7 s** |

The dev host is much faster than a 2 vCPU runner (lifecycle 146 s vs 223 s), which
is why the PR critical path is the lifecycle job.

## 6. What the first real CI run found (the reason this had to be triggered)

1. **Nightly failed on the worker image build.** `docker/Dockerfile.engines` used
   `FROM parsedock:0.1.0`, a tag that only exists in the local daemon. Under the
   buildx container driver the engine tried to pull
   `docker.io/library/parsedock:0.1.0` → `pull access denied`, failing
   `container-smoke` and `licence-audit`.
   Fixed by `ARG BASE_IMAGE` + `scripts/build-images.sh` and by using the plain
   Docker builder for the worker image (documented in `docs/CI.md`).
   A local `docker build` would have masked this because the tag happened to exist.
2. **SDK drift check failed on the first implementation.** Three causes:
   Java's `@Generated(date=…)`, generating over an existing tree (the generator
   deletes files listed in the previous FILES manifest, which removed generated
   tests), and CRLF differences from a `core.autocrlf=true` checkout. Fixed in
   `01de1a9` and enforced by `make sdk` + `.gitattributes`.
3. **Artifacts contained stale evidence.** `ci-local.sh` copied
   `benchmark/results/raw/acceptance-real-*.json` by glob, so the nightly artifact
   carried historical runs including FAILs from earlier local experiments. Now the
   collector only copies files newer than a phase-start marker (`1200ad4`).
4. **Node 20 deprecation warnings** for the first action pins → bumped to the
   Node 24 releases (`e9c43b2`).

## 6b. Merge gate: verified with a negative control

Requirement 3 ("a failing contract must prevent merging") was tested by pushing a
throwaway branch that renames the public ACK path (`/ack` → `/acknowledge`) and
opening draft PR #2. Results:

| Attempt | `contract` | `ci-summary` | Finding |
| --- | --- | --- | --- |
| run [41040541150](https://github.com/luxiang0412/ParseDock/actions/runs/41040541150) | fail (29 s) | **pass — false green** | `echo "$RESULTS" \| grep -qv '=success'` is a substring test: `contract=failure … static=success` still contains `=success` |
| run [41041186706](https://github.com/luxiang0412/ParseDock/actions/runs/41041186706) | fail (31 s) | **fail (5 s)** with `::error::contract: failure` | fixed by `scripts/ci_summary.py` + `tests/unit/test_ci_summary.py` (10 tests); the second attempt also exposed that `ci-summary` had no checkout step and could not run the script |

PR #2 was closed and its branch deleted after the experiment.

**Enforcement status: PARTIAL — platform blocked.** This repository is private on a
free GitHub plan, and both branch protection and repository rulesets are refused:

```
HTTP 403: Upgrade to GitHub Pro or make this repository public to enable this feature.
```

The check names, required list and failing behaviour are ready; the one-command
enablement (once the owner upgrades to Pro or makes the repo public) is:

```bash
gh api -X PUT repos/luxiang0412/ParseDock/branches/main/protection --input - <<'JSON'
{"required_status_checks":{"strict":true,
  "contexts":["static","unit","contract","lifecycle","image","ci-summary","engine-changes/detect"]},
 "enforce_admins":true,"required_pull_request_reviews":null,"restrictions":null,
 "allow_force_pushes":false,"allow_deletions":false,"required_conversation_resolution":true}
JSON
```

Until then a failing pipeline is clearly red on the PR (checks + `ci-summary`), but
a human with write access could still merge. That is a plan/visibility decision for
the owner, not a CI defect.

## 7. Risks and open items

| # | Risk | Mitigation / status |
| --- | --- | --- |
| R1 | GitHub-hosted 2 vCPU makes `lifecycle` the critical path (3 m 43 s); growth of the acceptance suite will erode the 8 min budget | keep the PR suite to lifecycle + PR-00 + backup; move heavier matrices (concurrency, long docs) to nightly; split the suite only when it exceeds ~5 min |
| R2 | No GPU runner: GPU workers are not exercised in CI | documented limitation; PR-08 owns a self-hosted GPU runner or a GPU-capable cloud job |
| R3 | Worker image cannot be published while `libx265` is present | `release-gate.yml` fails on tags; control plane (94.8 MB, GPL-free) can be published today |
| R4 | Docker layer cache is not used for the worker image (buildx cannot see the local base) | nightly rebuilds both images (~85 s); revisit if a registry becomes available (push base image, then buildx works) |
| R5 | Action SHAs must be refreshed when GitHub deprecates a runtime | `verify_pins.py` accepts only SHAs; bumping is a one-line change per action; Dependabot can be enabled later (not part of PR-01) |
| R6 | Real-engine download (590 MB) depends on GitHub/HF availability | cached by `actions/cache` keyed on `images.lock.json`; a failed download fails the nightly, not PRs |
| R7 | Coverage is not uploaded (Go/Python) | out of scope for PR-01; PR-09 (observability) is the natural home |
| R8 | `engine-changes` real-engine job is not a required check (path-classified) | documented; reviewers must see it green for engine PRs; making it required would block unrelated PRs |
| R9 | Branch protection / rulesets unavailable on a private free-plan repo (`403`) | see §6b: checks are red on failure and verified by a negative control; the enablement command is prepared; requires the owner to upgrade or make the repo public |

## 8. PR-00 leftovers found (recorded, not fixed here)

| Item | Impact | Recommendation |
| --- | --- | --- |
| SDK generation was not reproducible (timestamps, in-place cleanup, CRLF) | PR-00 published generated SDKs that could not be verified for drift | **fixed in PR-01** as a prerequisite for the drift gate |
| `docs/DEPLOYMENT.md` told operators to `make images`; the worker image silently depended on a locally tagged `parsedock:0.1.0` | undocumented build-order dependency; broke CI | **fixed**: `scripts/build-images.sh` is now the single place that knows the relationship, and the Dockerfile takes `BASE_IMAGE` |
| `IMPLEMENTATION-PLAN` PR-01's `make verify-pins` did not exist | drift was unverifiable | **fixed**: `scripts/verify_pins.py` + Makefile target |
| `scripts/license_audit.sh` needed a non-failing mode for CI signal | would have made every PR red | **fixed**: `AUDIT_MODE=report` vs `enforce` |

No PR-00 *behavioural* defect was found; the runtime contract tests are unchanged
and green.

## 9. Definition of Done checklist

- [x] Go vet/test/build, OpenAPI validation, public+internal contract, SDK drift and
      PR-00 regression tests run in GitHub Actions.
- [x] Mock lifecycle acceptance on every PR; real engines nightly and on
      engine-related changes (`engine-changes` path classification).
- [ ] **Required status checks enforced on `main`** — **PARTIAL (platform)**. The
      gate exists and was proven to fail the pipeline (`ci-summary` red with
      `contract=failure`), but GitHub refuses branch protection/rulesets for a
      private repo on a free plan (`HTTP 403`, §6b). Enablement command prepared.
- [x] Reproducible builds: pinned Go 1.24.4, `go.sum`, image digests,
      `-trimpath -buildvcs=false -buildid=`, byte-identical proof in CI.
- [x] Artifacts: full logs, step-timing JSON, acceptance/PR-00 JSON, stack logs,
      image digest, repro hashes, SBOM, licence classification.
- [x] PR pipeline under 8 minutes (3 m 53 s) with `setup-go` caching and engine/model
      caching for the nightly job.
- [x] Licence audit distinguishes control-plane PASS and worker BLOCK; release gate
      blocks publication.
- [x] No API/ACK/TTL/worker-protocol/IR changes; PR-00 suites unchanged and green.
- [x] CI triggered for real (5 runs; 1 failure investigated and fixed).

## 10. Recommendation

**GO for PR-02** (security hardening of the public edge). Conditions that stay open
and do not block PR-02:

1. **L1 licence decision** — worker image publication remains blocked; the control
   plane can ship.
2. **GPU and two-host verification** — remain PR-08 / PR-07 and are not covered by
   CI (documented as limitations, not claimed as passing).

Suggested PR-02 CI additions: keep the six required jobs; add quota/key-rotation
cases to `tests/contract`/`tests/integration` and they will run on every PR without
changing the pipeline shape.
