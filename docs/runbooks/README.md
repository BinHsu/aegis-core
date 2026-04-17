# Runbooks — Aegis Core

Operational procedures that require manual human action (account
creation, credential rotation, cross-repo coordination). Runbooks
live here — NOT in CONTRIBUTING.md, NOT in ADRs, NOT in
`docs/incidents.md` — so the click-by-click steps stay separate
from:

- **ADRs** (`docs/adr/`): decision rationale, trade-offs, architecture
- **Incidents** (`docs/incidents.md`): postmortems for blockers
- **CONTRIBUTING.md**: PR workflow and developer setup for code changes
- **CLAUDE.md**: AI agent ironclad rules

## Audience scoping

Every runbook starts with an **Audience** section declaring who the
procedure applies to. Typical audiences:

| Audience | Meaning |
| --- | --- |
| **Upstream repo operator** | The maintainer of `BinHsu/aegis-core` running the canonical CI pipeline. One-time steps needed before the upstream GitHub Actions workflow can succeed. |
| **Fork operator** | Someone who forked this repo and wants to run their own CI with cache infrastructure. Steps are the same, but against their fork's secrets / their own cloud accounts. |
| **Casual cloner** | Someone who just wants `bazel build` locally. Runbooks in this folder **do not apply** — local builds are fully hermetic, no cloud signup required. |

If a runbook does not apply to you as a reader, skip it. Local
cloning + building never requires following any runbook in this
folder.

## Index

- [`buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) —
  one-time BuildBuddy Personal free-tier signup + API key + GHA
  secret wiring (ADR-0014 Option β, Phase A demo horizon).
  **Audience**: upstream repo operator; fork operator (optional).

## When to add a new runbook

- A procedure requires manual human action that an AI agent cannot
  perform (account creation, cloud resource provisioning, secret
  handling).
- The procedure involves third-party UIs whose click paths would
  otherwise be rediscovered each time.
- The procedure has a rotation / revoke flow that needs to be just
  as documented as the initial setup.

Ephemeral one-shot ops (a single `gh` command) do NOT warrant a
runbook — put them in the PR description or commit message.
