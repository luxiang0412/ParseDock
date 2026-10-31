# Code audits

Three whole-repository audits were run with `codex exec -s read-only` (static analysis, no
tests executed by the reviewer) after Phase 1, plus a final verification pass. Every finding
was checked against the source before being acted on; findings that could not be reproduced in
the code were dropped rather than "fixed" blindly.

| Pass | Findings | Outcome |
| --- | --- | --- |
| 1 | 16 | 6 fixed in batch A, 4 in batch B, 3 in batch C, 1 documented as a design change (OCR parallelism) |
| 2 (verification of batches A–C) | 11 new + 6 still open | fixed in batch D, except the items listed below |
| 3 (verification of batch D) | 6 new + 4 still open | fixed in batch E |
| 4 (final, scoped) | 0 blocking, 3 "incomplete" | fixed in batch F |

## What the audits changed

The defects were real and none were theoretical:

* **P0** — orphan cleanup treated *any* database error as "job does not exist" and deleted the
  directory; a shutdown that cancelled the cleanup context would have removed live inputs and
  results.
* **P1** — ACK accepted a queued/running job with an arbitrary `result_version`, authorising
  deletion of an in-flight job's input; ACK and cancel now require a completed result and an
  exact version, repeated inside the UPDATE.
* **P1** — a failure report from a superseded attempt could fail a job another worker was
  parsing; both `Fail` updates are fenced by `current_attempt` + lease token.
* **P1** — rendering allocated `Rows × Cols` from unvalidated IR in the **coordinator**
  (~20 billion slots from a 500 KB ragged CSV), and the guard could be bypassed with duplicate
  table IDs; validation now bounds declared *and* referenced area and rejects duplicate IDs.
* **P1** — two completions for one job shared a result directory, so the loser could overwrite
  and then delete the winner's files. Results are now content-addressed generations, and
  publication/reclamation are serialised per job.
* **P1** — result staging used shared `tmp/<job>.result.<name>.part` names, so concurrent
  attempts truncated each other's files.
* **P1** — the lease reaper did not re-check expiry and was not fenced by `current_attempt`; a
  heartbeat that landed in the gap could still be reaped.
* **P1** — a rejected result left the job `running` until its lease expired, so it was
  re-parsed from scratch on each attempt (a 10 MB ledger burned 348 s and then failed).
* **P1** — uploads ran under the process context, not the job deadline; a stalled upload could
  outlive its lease.
* **P1** — timestamps were stored by `RFC3339Nano` (variable width) but compared as TEXT, so
  lease expiry could compare the wrong way; fixed-width storage plus `julianday()` for leases.
* **P1** — a client that stopped reading blocked the result stream forever while the lease
  renewer kept the result alive; responses now have a 60 s write-idle deadline.
* P2 — plain text truncated at a 1 MiB line; the subprocess output limiter returned a short
  write; PDF pre-flight double-counted (then lost) page objects at a sample boundary; Markdown
  tables collided by row count; page merges renamed tables without rewriting references (and
  later, rewrote them twice); backup restore announced success without verifying; redacted
  corpus reports leaked engine error strings.

## Deliberately not changed

* **Page-parallel OCR.** The `xberg` CLI exposes no page-range or thread option, so the 77 s
  scan is OCR-bound by design. Splitting pages across processes needs ID/page/Range remapping;
  a serve-mode adapter is a new component to operate. Both are decisions, not tuning.
* **Worker image redistribution** — blocked on the L1 licence (`libx265`, GPL-2.0).
* **Legacy result migration.** Pre-generation result directories are served through a
  version-checked fallback rather than migrated in place; they are removed with the job.

## How to reproduce

```bash
# the audits themselves (read-only, needs the codex CLI)
codex exec -s read-only "audit this repository for correctness, concurrency, resource and
security defects; prioritise findings with file:line and the smallest correct fix"

# what the fixes must keep true
go test ./...
bash scripts/ci-local.sh all
python3 tests/integration/pr12_drill.py      # backup + restore + RTO
```
