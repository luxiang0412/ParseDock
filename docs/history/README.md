# docs/history — point-in-time reports

Reports, designs and surveys that describe the repository **as it was on the date shown**.
They are kept as evidence (what was measured, what was decided and why) and are
**historical — not maintained**: flags, defaults, file paths and numbers in them may have
changed since. The current state lives in the living documents in `docs/` (start at
`docs/PHASE1-SUMMARY.md`, `docs/ARCHITECTURE.md`, `docs/DEPLOYMENT.md`, `docs/CHANGELOG.md`).

| File | What it is | Date / status |
| --- | --- | --- |
| `PHASE0-SUMMARY.md` | Phase 0 final report (results, risks, next PRs); scores superseded by PR-00 | 2026-10-30 · historical — not maintained |
| `PHASE1-READINESS.md` | PR-00 result: per-finding fixes for the Phase 1 readiness review, GO/NO-GO | 2026-10-30 · historical — not maintained |
| `PR00-ISSUE-MATRIX.md` | PR-00 verification of findings F01–F11 against the code | 2026-10-30 · historical — not maintained |
| `PR01-REPORT.md` | PR-01 CI baseline report: real GitHub Actions runs, branch-protection caveat (§6b) | 2026-10-30 · historical — not maintained |
| `GPU-REPORT.md` | GPU measurements (RTX 2080 Ti) and the GPU worker image defects found by running it | 2026-10-30/31 · historical — not maintained |
| `AUDITS.md` | three post-Phase-1 whole-repository audits: findings, fixes, deliberate omissions | 2026-10-31 · historical — not maintained |
| `PLAYGROUND-READINESS.md` | read-only survey before the playground was built (API surface, preview options) | 2026-10-31 · historical — not maintained |
| `PLAYGROUND-TEST-REPORT.md` | playground evidence: unit/E2E/security runs, screenshots | 2026-10-31 · historical — not maintained |
| `OCR-BACKEND-RESEARCH.md` | OCR backend and GPU acceleration spike (source-level research) | 2026-10-31 · historical — not maintained |
| `OCR-GPU-BENCHMARK.md` | OCR benchmark: CPU backends and GPU A/B | 2026-10-31 · historical — not maintained |
| `OCR-ROUTING-PROPOSAL.md` | OCR routing proposal that led to PR-A/B/D | 2026-10-31 · historical — not maintained |
| `OCR-PR-A-REPORT.md` | PR-A: opt-in xberg OCR backend (`--xberg-ocr-backend`) | 2026-10-31 · historical — not maintained |
| `OCR-PR-B-DESIGN.md` | PR-B design: OCR quality semantics and escalation (implemented) | 2026-10-31 · historical — not maintained |
| `OCR-PR-B-REPORT.md` | PR-B report: OCR quality signals and escalation, measured | 2026-10-31 · historical — not maintained |
| `OCR-PR-D-DESIGN.md` | PR-D design: `gpu_required` must prove GPU inference (stage 1 implemented, stages 2–3 open) | 2026-10-31 · historical — not maintained |
| `OCR-PR-D1-REPORT.md` | PR-D1: per-task GPU execution evidence for `gpu_required` | 2026-10-31 · historical — not maintained |
| `OCR-PR-D2-REPORT.md` | PR-D2: GPU worker image Vulkan prerequisites; blocked on the test host | 2026-10-31 · historical — not maintained |

The current OCR/GPU decision record is `docs/OCR-GPU-DECISIONS.md`; the engine decision is
`docs/ENGINE-DECISION.md`; the ADR log is `docs/DECISIONS.md`.
