# urunc × Argo Workflows integration — issue #573

Supporting material for an LFX mentorship application on
[urunc-dev/urunc#573](https://github.com/urunc-dev/urunc/issues/573): integrating urunc's
sandboxed unikernel execution with Argo Workflows.

## The code

The integration itself lives on a branch of my urunc fork:
**[main…argo-poc-integration](https://github.com/urunc-dev/urunc/compare/main...x15sr71:urunc:argo-poc-integration?expand=1)**
(3 production files + 2 test files). The same changes are mirrored here as a patch in
[`poc/`](poc).

## Start here

- **[EXERCISE_SUMMARY.md](EXERCISE_SUMMARY.md)** — the pre-application exercise: set up urunc,
  set up Argo, replicate the issue.
- **[TUTORIAL.md](TUTORIAL.md)** — how to deploy and run an Argo workflow on urunc: prerequisites,
  build, configuration, and a worked example.
- **[ARCHITECTURE.md](ARCHITECTURE.md)** — full architecture doc: Argo's execution model, urunc's
  execution model, and a detailed incompatibility breakdown.
- **[COMPATIBILITY_AND_ROADMAP.md](COMPATIBILITY_AND_ROADMAP.md)** — a shorter, corrected companion
  to the architecture doc; supersedes it wherever the two disagree.

## Evidence

- **[REPRO_RESULTS.md](REPRO_RESULTS.md)** — the original cluster reproduction of the hang.
- **[RUNBOOK.md](RUNBOOK.md)** — rebuilding the whole stack (urunc + k3s + Argo) from a clean box.
- **[LIVE_VALIDATION.md](LIVE_VALIDATION.md)** — raw, unedited command output from a live
  re-verification pass.
- **[SECURITY_VALIDATION.md](SECURITY_VALIDATION.md)** — threat-model checks against the prototype
  (symlink handling, size caps, non-destructive testing methodology).
- **[STATUS.md](STATUS.md)** — current status of the prototype: what's implemented, what's tested,
  what's explicitly still open.
- **[code_audit/](code_audit)** — source-level audit notes and cited snippets.
- **[vm_logs/](vm_logs)** — raw evidence dump from the test VM.

## Prototype

- **[poc/](poc)** — the working prototype's diff and its test files (shim-side exit-code writing,
  static networking for Argo pods, best-effort output extraction, plus two robustness fixes around
  concurrent-delete handling and atomic file writes).
- **[manifests/](manifests)** — runnable manifests, including
  [`urunc-argo-example.yaml`](manifests/urunc-argo-example.yaml), the worked example from the
  tutorial (verified `Succeeded`).

## Status

This is a mentorship *application*, not a merged contribution — the maintainers have asked that
mentorship-issue work not be submitted as PRs before the mentorship term starts. Everything here is
pre-work: diagnosis, a tested prototype, and a proposal for how to take it further.
