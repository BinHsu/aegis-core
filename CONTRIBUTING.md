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

# REQUIRED: install BOTH git hooks. Without these, your local commits
# will look green to you but will be blocked by CI, burning a round-trip
# per commit. The pre-commit hook catches formatting / secrets / proto
# lint; the commit-msg hook enforces Conventional Commits.
pre-commit install
pre-commit install --hook-type commit-msg

# Sanity check — should print "Passed" / "Skipped" for every hook.
pre-commit run --all-files
```

**If `pre-commit` isn't installed yet**:

```bash
pipx install pre-commit              # preferred (isolated)
# or
pip install --user pre-commit        # fallback
```

**Why both hooks are required**:

| Hook type         | What it catches                                         |
|-------------------|---------------------------------------------------------|
| `pre-commit`      | trailing whitespace, secret leaks (gitleaks), clang-format drift, `buf lint`, `buf format`, `buf breaking`, prettier, `go fmt`/`vet`, YAML/JSON validity, large file guard |
| `commit-msg`      | Conventional Commits format (`feat(scope): ...`)        |

The **same `.pre-commit-config.yaml` runs in CI** as a belt-and-braces
second pass. Skipping the local install does not leak bad code to `main`
(ruleset + CI block the push), but every skipped local check is a wasted
CI cycle — fixed formatting means an extra round-trip of push →
CI-fail → format → re-push → CI-retry.

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

### Running Go tools (no system Go required)

Aegis Core does not require a system Go install — the Go 1.24.12 SDK is
hermetic via `rules_go`. Two wrapper scripts surface the underlying
toolchain without leaving the repo:

```bash
./tools/scripts/go.sh fmt ./...     # run from inside a Go module root
./tools/scripts/go.sh vet ./...
./tools/scripts/go.sh mod tidy      # from inside gateway_go/

./tools/scripts/go_check.sh         # run gofmt + govet across every
                                    # module listed in go.work. Suitable
                                    # as a pre-PR smoke test.
```

Why this pattern instead of `pre-commit`-driven Go hooks? The widely-used
`dnephin/pre-commit-golang` hooks shell out to a system `go` binary,
which violates [CLAUDE.md Rule 6](CLAUDE.md). Running Go through
`tools/scripts/go.sh` keeps every byte of toolchain state inside the
repo tree.

### LAN smoke — full RAG stack end to end

Exercises the full Local-mode pipeline: mic → WebRTC → gateway → engine →
Whisper ASR → bge-m3 embedding → Qdrant → PrompterHint → WebSocket →
viewer UI. Demo bar: speak `what's the weather like in Taiwan` into the
host's mic and see both the transcript and a matching Taiwan-corpus hint
appear on the phone viewer.

This path is human-in-the-loop (a mic and a pair of ears are part of the
test rig), so it is not covered by CI. The script below fails fast at
every service-level prerequisite; the final "talk into the mic" step is
yours.

**Prerequisites** (one-time, ~5 min):

```bash
# Embedder: bge-m3 Q4_K_M (438 MB). required=false in the manifest, so
# download_models.sh's default run skips it — pass --model explicitly.
./tools/scripts/download_models.sh --model bge-m3-q4km

# Qdrant: follow docs/runbooks/qdrant-local-setup.md to get a server
# listening on localhost:6334. Keep that terminal open.
```

**Seed + run** (idempotent; re-run whenever the corpus changes):

```bash
./tools/scripts/lan-smoke.sh
# Follow the printed instructions for the final `bazel run //:app_local`
# step. QDRANT_URL must be exported in the shell that runs app_local —
# the launcher inherits it via os.Environ() and passes it through to
# the engine child (see gateway_go/cmd/app_local/main.go startChild).
```

**Drive it**:

1. Open `http://localhost:5173` (the host UI served by Vite when
   `--with-frontend` is on).
2. Create a meeting with `rag_id=aegis_taiwan` — the collection name is
   derived from the corpus filename by `DeriveCollectionName()` in
   `engine_cpp/cmd/engine/seed.cc`.
3. Scan the viewer QR with a phone on the same LAN.
4. Speak into the mic. Every 3 s window produces a transcript segment
   and triggers a RAG retrieval; hints fire on every window today (no
   question gate, no score threshold) — documented in `ROADMAP.md`
   Phase 4c as a UX-noise item, not a correctness bug.

**If `QDRANT_URL` is unset** when you run `app_local`, the engine logs
`QDRANT_URL unset (RAG hints disabled)` and falls through to
transcript-only mode. That is the intentional fail-closed behaviour — the
same binary that runs in Cloud today (where the K8s manifest also does
not set `QDRANT_URL`, tracked as a Phase 4c follow-up in ROADMAP.md).

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

## Upgrading the ggml triple (ADR-0021)

The three `http_archive` entries in `MODULE.bazel` — `ggml`,
`whisper_cpp`, `llama_cpp` — form a **version-coupled triple** per
[ADR-0021](docs/adr/0021-shared-ggml-runtime.md). They share one ggml
runtime and must be bumped together. Dependabot is excluded from them
(see `.github/dependabot.yml`) because automated independent bumps
break the build.

### When to upgrade

- A security advisory lands on any of the three upstreams.
- You need a ggml/whisper/llama feature only in a newer release
  (new quantization format, new model architecture, perf fix).
- Routine hygiene, scoped to a single PR, at most monthly.

### Procedure

1. **Check upstream tags.** The canonical source for all three:
   - `ggml`          — <https://github.com/ggml-org/ggml/tags>
   - `whisper.cpp`   — <https://github.com/ggml-org/whisper.cpp/tags>
   - `llama.cpp`     — <https://github.com/ggml-org/llama.cpp/tags>

2. **Pick a compatible triple.** Open each consumer's bundled
   `ggml/CMakeLists.txt` at the tag you want, read the
   `GGML_VERSION_MAJOR/MINOR/PATCH` values, and confirm:
   - Standalone `@ggml` version **≥** the version declared by
     `whisper.cpp`'s bundled ggml, **AND ≥** the version declared by
     `llama.cpp`'s bundled ggml.
   - `ggml-org/whisper.cpp` and `ggml-org/llama.cpp` occasionally
     cherry-pick ggml patches **ahead** of the standalone release (see
     [`docs/incidents.md`](docs/incidents.md) #10). The standalone
     `@ggml` must be new enough that every symbol the consumers
     reference is exported. When in doubt, pick the newest standalone
     ggml tag with a release date ≥ both consumers' release dates.

3. **Compute SHA-256s.** For each of the three tarballs:

   ```bash
   curl -sL https://github.com/ggml-org/<repo>/archive/refs/tags/<TAG>.tar.gz \
     | shasum -a 256
   ```

4. **Update `MODULE.bazel`.** Edit all three `http_archive` entries in
   a single commit: `sha256`, `strip_prefix`, and `urls`. Update the
   banner comment's `Current pin` lines to match.

5. **Run the drift check + full build locally.**

   ```bash
   ./tools/scripts/check_ggml_versions.sh
   ./tools/bazelisk/bazelisk build //engine_cpp/tests/integration/...
   ./tools/bazelisk/bazelisk test //engine_cpp/...
   ```

   The first command parses each archive's `GGML_VERSION_*` and fails
   if the standalone is older than a consumer's bundled ggml. The
   integration build is the authoritative link-compatibility gate; it
   catches the "same version number, divergent source" drift that the
   version-string check alone cannot (ADR-0021 P3, incident-10).

6. **PR conventions.**
   - Title prefix: `deps(ggml-triple):` to signal the coupled upgrade.
   - Body must state the selected triple, the reason (security / perf /
     feature), and confirm the integration-build gate passed.
   - A single commit is preferred; revert must bump all three back in
     lockstep.

### What to do when the drift check fails

If `check_ggml_versions.sh` reports standalone `@ggml` **older** than a
consumer's bundled ggml, **do not** downgrade the consumer — that path
loses upstream features and security fixes. Instead, bump standalone
`@ggml` to a newer tag that covers both consumers' expected API. If no
such tag exists (the consumer was cut from a ggml master SHA that
hasn't been tagged yet), pin `@ggml` at a specific commit SHA rather
than a tag, and note the deviation in the MODULE.bazel banner.

Typical effort: 20–40 minutes for a routine bump; longer if an upstream
API break requires patching engine code.

## Remote cache (optional, CI only)

Local `bazel build` does not use any remote cache. The committed
`.bazelrc` has no `--remote_cache` directive — cloning and building
from source works with zero cloud signup, reading from and writing
to `./.bazel_cache/` on your own disk, with all dependencies
downloaded hermetically per CLAUDE.md Rule 6. A developer-local
override is possible via `.bazelrc.user` (gitignored; the
`.bazelrc:90` `try-import` picks it up).

### What CI does

CI layers an opt-in Bazel remote cache on top of `actions/cache`
(the latter remains as the no-internet fallback). The full cache
strategy — including the trade-off analysis and migration triggers
— is in [ADR-0014](docs/adr/0014-bazel-build-cache-strategy.md).

**Current (demo horizon)**: Option β — BuildBuddy Personal free tier.
An API key lives in GitHub Actions secrets as `BUILDBUDDY_API_KEY`;
the CI workflow passes it via
`--remote_header=x-buildbuddy-api-key=…`. The one-time signup +
key + secret wiring is a manual-human-action procedure captured in
[`docs/runbooks/buildbuddy-cache-setup.md`](docs/runbooks/buildbuddy-cache-setup.md)
— upstream operators follow it once; fork operators follow it only
if they want their own cache namespace.

**Planned (production)**: Option δ — S3 direct via Bazel 7.4+
`--credential_helper`, short-lived AWS creds via GitHub Actions OIDC
federation. β→δ migration triggers are in ADR-0014 §Decision
Outcome.

### If you fork this repo

Remote cache is an optimization, never a correctness dependency.
Three common postures for a fork:

- **Use your own BuildBuddy cache** — follow
  [`docs/runbooks/buildbuddy-cache-setup.md`](docs/runbooks/buildbuddy-cache-setup.md)
  against your fork's GitHub repository; the existing workflow
  will use your namespace automatically.
- **Disable remote cache entirely** — remove the `--remote_cache`
  / `--bes_backend` / `--remote_header` flags from
  `.github/workflows/ci-baseline.yml`. `actions/cache` stays; you
  only lose the cross-PR cache tier.
- **Bring your own S3 + OIDC** — follow ADR-0014 §"δ prerequisites"
  for the IAM role / OIDC trust policy / bucket config spec. Swap
  the `--remote_cache` flag to your S3 URL and wire the credential
  helper.

### β→δ migration is a cross-repo event

When this repo migrates β→δ, it is a coordinated change with the
sibling `aegis-aws-landing-zone` repo per
[README §Cross-repo coordination ritual](README.md#cross-repo-coordination-ritual):
the sibling owns the S3 bucket, the dedicated
`github-actions-aegis-core` IAM role, and the OIDC trust policy
(full spec in ADR-0014 §"δ prerequisites — what
`aegis-aws-landing-zone` must provide"). The migration opens a
`cross-repo/blocking` issue on `aegis-aws-landing-zone` first,
waits for the sibling to provision resources, then swaps the CI
flag in a single PR here. Do not self-migrate before the sibling
confirms the resources exist; CI will fail on every run until
credentials resolve.

## Native Windows support (known gap)

Tier-1 development environments are **macOS** and **Linux**. Windows
users: **use WSL2** — from Bazel's perspective WSL2 is Linux, and every
Ubuntu command in this doc works 1:1 inside it. Native Windows (cmd or
PowerShell without WSL) is not tested, CI does not cover it, and we
will not merge a PR that claims Windows support without the concrete
changes below.

Native Windows support is welcome as a community contribution. Before
you start, know what would need to change:

1. **`tools/bazelisk/bazelisk` is a bash script** — Windows needs a
   `bazelisk.ps1` or `bazelisk.bat` sibling preserving the same
   contract: pin Bazel 7.4.1, route `--output_user_root` inside the
   repo tree (CLAUDE.md Rule 6), handle paths with spaces.
2. **Shell scripts under `tools/scripts/`** — `download_models.sh`,
   `check_ggml_versions.sh`, `proto_gen.sh`, `frontend.sh`. Each
   needs a Windows-compatible counterpart or a PowerShell rewrite.
3. **rules_foreign_cc + MSVC** — whisper.cpp / ggml / llama.cpp have
   upstream Windows CMake configs but our `*.BUILD` files bake in
   `CMAKE_OSX_DEPLOYMENT_TARGET=11.0` and Apple-framework linkopts.
   Windows needs a `select()` branch. First build is likely to cold-
   build BoringSSL + gRPC + Abseil on MSVC, which often surfaces
   new warnings / failures upstream has not triaged.
4. **Bazel on Windows symlink behavior** — Bazel's sandbox wants
   symlinks; Windows requires Developer Mode or admin to create them.
   Document the Developer Mode toggle in a runbook rather than in
   PR changelog.
5. **`.github/workflows/ci-baseline.yml`** — add a `windows-latest`
   matrix entry to the `bazel-unit-tests` job so the support is
   validated on every PR, not a one-off "works on my machine."
6. **pre-commit hooks** — Python-based so Windows-compatible in
   principle, but several hooks shell out (`gitleaks` behaves; some
   `buf-breaking` paths assume POSIX). Run `pre-commit run
   --all-files` on Windows as the acceptance bar.

If you want to take this on, open an issue first so we can scope it
together — the work naturally lands in 2–3 PRs (wrapper script,
BUILD `select()` branches, CI matrix + a first-run stabilization
pass) rather than one giant bundle.

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
