# Contributing to Aegis Core

Thank you for your interest in contributing. This document describes the
development setup, pull request conventions, and review expectations.

**Before anything else, read these in order:**

1. [`CLAUDE.md`](CLAUDE.md) — the ironclad engineering rules, including
   language conventions, directory confinement, and test integrity. These
   rules apply to human contributors and AI agents equally.
2. [`ARCHITECTURE.md`](ARCHITECTURE.md) — system topology, data flow,
   governance, and known limitations.
3. [`ROADMAP.md`](ROADMAP.md) — what phase the project is in and what the
   current priorities are.
4. [`docs/adr/`](docs/adr/) — every accepted architecture decision, with
   full context and trade-offs. **Do not re-litigate an accepted ADR
   without a new ADR that supersedes it.**
5. [`SECURITY.md`](SECURITY.md) — if you are reporting a vulnerability,
   follow the private disclosure process there, not a public issue.

## Development Setup

### Prerequisites

Aegis Core uses Bazel as the single source of truth for all builds. You
do **not** need to install Bazel, Go, Node.js, or a C++ toolchain globally
— Bazel fetches hermetic toolchains into `./.bazel_cache/` per
[CLAUDE.md Rule 6](CLAUDE.md).

You do need, once per clone:

- **Git** (system)
- **Python 3.10+** (for running pre-commit hooks)
- **pre-commit** — install via `pipx install pre-commit` or
  `pip install --user pre-commit`

### First-time setup

```bash
git clone https://github.com/BinHsu/aegis-core.git
cd aegis-core

# Install pre-commit hooks (runs automatically on every git commit)
pre-commit install
pre-commit install --hook-type commit-msg

# Sanity check that hooks work
pre-commit run --all-files
```

During Phase 1+ when Bazel targets exist, you will also run:

```bash
# Build everything
./tools/bazelisk build //...

# Run a local development stack
./tools/bazelisk run //:app_local
```

### Running pre-commit manually

```bash
pre-commit run --all-files          # run all hooks on every file
pre-commit run --files path/to/f    # run on specific files
pre-commit run clang-format         # run one hook across staged files
```

## Pull Request Conventions

### Branch naming

```
feat/<short-description>
fix/<short-description>
docs/<short-description>
refactor/<short-description>
test/<short-description>
chore/<short-description>
```

Example: `feat/session-registry-pause-resume`.

### Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
The `.pre-commit-config.yaml` enforces this via a `commit-msg` hook.

**Format**:

```
<type>(<scope>): <subject>

<body>

<footers>
```

**Types** (enforced): `feat`, `fix`, `docs`, `refactor`, `test`, `chore`,
`build`, `ci`, `perf`, `revert`.

**Example**:

```
feat(gateway_go): implement WebRTC ingress with Pion

- Accept SDP offer from host, terminate WebRTC at GW
- Unwrap UDP into PCM and forward over gRPC to engine
- Add keepalive timers matching ADR-0006

Refs: ADR-0006
```

**Rules**:

- Subject line is imperative mood ("add X", not "added X").
- Subject line is ≤ 72 characters.
- Body explains **why**, not just what.
- Footer may include `Refs: ADR-XXXX` or `Closes: #issue`.
- The `Co-Authored-By:` trailer is used when pair-programming or when
  AI assistance produced a substantial portion of the change.

### Architecture changes require an ADR

If your PR introduces:

- A new trust boundary, data flow, or external integration
- A new language, framework, or major dependency
- A change to any decision already recorded in `docs/adr/`
- A new storage class or persistence layer

…you **must** include a new ADR in `docs/adr/NNNN-short-title.md` as part
of the PR. Use the existing ADRs as templates. Supersession is done by
adding a new ADR that references the old one; **do not rewrite an
accepted ADR in place**.

### Architecture changes require a threat model update

If the change affects the data flow, trust boundary, or asset list in
`docs/threat-model.md`, update that file in the same PR. Reviewers MUST
verify the threat model is still accurate.

### Code review

All PRs require:

- At least one approving review from a CODEOWNER
- All CI checks green
- Pre-commit hooks passing locally (enforced by hooks anyway)
- No unresolved review comments

The reviewer is expected to verify:

1. The change matches what the PR description says.
2. Tests exist and exercise the real behavior (CLAUDE.md Rule 2 — no stub
   tests that just `return true`).
3. Any architectural implications are captured in ARCHITECTURE.md or an
   ADR.
4. Documentation (including `README.md` if user-facing commands change)
   is updated.

## Testing Expectations

See [`ARCHITECTURE.md`](ARCHITECTURE.md) §10.5 for the full test strategy.
Minimum expectations:

- **Unit tests**: for any non-trivial function or class.
- **Integration tests**: when crossing a module boundary.
- **Contract tests**: any change to `proto/` must pass `buf breaking`.
- **WER golden audio regression**: changes touching the transcription
  path must pass the WER suite in CI (see ADR-0011).
- **No stub tests**: per CLAUDE.md Rule 2, every test must drive real
  inputs through real code and verify real outputs. A test that returns
  `true` unconditionally is worse than no test because it provides false
  confidence.

## Language Conventions

- **All code, comments, commit messages, file names, and project
  documentation MUST be in English** per CLAUDE.md Rule 5.
- Conversational chat with the maintainer or in issues / discussions
  may use the maintainer's native language.
- If you are translating existing content, flag the translation clearly
  in the PR description.

## Directory Confinement

Per CLAUDE.md Rule 6, **every dependency, cache, virtualenv,
`node_modules`, and model file must stay inside the repository tree**.
The `.gitignore` and `.bazelrc` enforce this. If you find yourself
wanting to install something globally (e.g., `brew install`,
`pip install` without `--user`), **stop** — find a way to do it inside
the repo, or raise an issue asking how.

## Getting Help

- **Technical questions**: open a Discussion in the "Q&A" category.
- **Bug reports**: open an Issue with the "bug" label and a
  reproduction.
- **Security vulnerabilities**: follow `SECURITY.md`, do NOT open a
  public issue.
- **Architecture clarification**: read the relevant ADR first; if it
  does not answer your question, open a Discussion in the "Ideas"
  category and propose an ADR update.

## AI Agent Contributors

Aegis Core explicitly welcomes contributions from AI coding agents, but
they are subject to CLAUDE.md's ironclad rules. In particular:

- Be honest about what you did and did not do.
- Do not fabricate test results or commit messages.
- Always read ARCHITECTURE.md and the relevant ADRs before writing code.
- Keep changes inside the repo tree; never touch global system state.
- Flag your contributions with the `Co-Authored-By: <Agent Name>`
  trailer in commits.

See CLAUDE.md for the full rules.
