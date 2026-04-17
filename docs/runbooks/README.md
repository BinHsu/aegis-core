# Runbooks — Aegis Core

Operational procedures that require manual human action, OR that
are too click-heavy / platform-specific / branching to fit in the
README without clutter. Runbooks live here — NOT in CONTRIBUTING.md,
NOT in ADRs, NOT in `docs/incidents.md` — so the step-by-step
mechanics stay separate from:

- **ADRs** (`docs/adr/`): decision rationale, trade-offs, architecture
- **Incidents** (`docs/incidents.md`): postmortems for blockers
- **CONTRIBUTING.md**: PR workflow and the happy-path setup that
  every contributor walks through (pre-commit, hooks, Conventional
  Commits). Anything that branches by OS, by account type, or by
  third-party UI belongs in a runbook, not here.
- **README.md**: the 3-minute "does this run?" story. Anything that
  requires a decision tree (e.g., "if you're on macOS without Xcode,
  do X, otherwise Y") belongs in a runbook — README stays clean.
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

### Repository administration (one-time, upstream operator)

- [`../github-setup.md`](../github-setup.md) — GitHub ruleset,
  required signatures, branch protection, SSH commit signing on
  macOS, Private Vulnerability Reporting, secret scanning push
  protection. Cross-referenced here because it predates this
  folder; the content is runbook-shaped (admin-only, click-by-click,
  `gh` CLI + UI fallback). A future refactor may move it under
  `docs/runbooks/` for consistency; no urgency.

### Third-party service onboarding (one-time, upstream or fork)

- [`buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) —
  one-time BuildBuddy Personal free-tier signup + API key + GHA
  secret wiring (ADR-0014 Option β, Phase A demo horizon).
  **Audience**: upstream repo operator; fork operator (optional).

### Local development troubleshooting

_(Empty by design.)_ The committed Quick Start in `README.md`
covers the happy path, and the hermetic toolchain via `bazelisk`
handles the common cases (path with spaces → auto-redirected cache;
first Bazel download → transparent). Runbooks here are added when a
concrete pain point shows up — today, the only documented
platform-specific pitfall is Incident 01 (macOS CLT-only Bazel
cascade), which the bazelisk wrapper fix (`tools/bazelisk/bazelisk`)
already handles for fresh clones. If you hit a setup failure not
covered by the Quick Start or the happy-path docs, file an issue;
that issue's resolution may earn a runbook entry here.

## When to add a new runbook

- A procedure requires manual human action that an AI agent cannot
  perform (account creation, cloud resource provisioning, secret
  handling).
- The procedure involves third-party UIs whose click paths would
  otherwise be rediscovered each time.
- The procedure branches by OS, account type, or tool version, and
  documenting it inline in README / CONTRIBUTING would bloat those
  files beyond their purpose.
- The procedure has a rotation / revoke flow that needs to be just
  as documented as the initial setup.

Ephemeral one-shot ops (a single `gh` command) do NOT warrant a
runbook — put them in the PR description or commit message.
