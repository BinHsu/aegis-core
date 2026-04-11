# ADR-0008: Monorepo Folder Structure

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core is a polyglot Bazel monorepo containing a C++ inference engine,
a Go BFF gateway, a TypeScript React frontend, and — from Phase 4+ — a
Rust Tauri desktop shell. The decision to house everything under a single
repository is already made by `ARCHITECTURE.md` §1 ("Entire project
exists in a single Bazel Monorepo"). What remains is the shape of that
monorepo: which directories exist at the root, how each component owns
its `BUILD.bazel` files, where cross-cutting assets (proto contracts,
model artifacts, golden audio fixtures) live, and how the layout respects
the hexagonal / Ports-and-Adapters boundaries defined by
`ARCHITECTURE.md` §5 and ADR-0002 Constraint 2.

This ADR settles that shape before Phase 1 starts laying down Bazel
scaffolding. Getting it right now costs nothing; renaming it after
twenty `BUILD.bazel` files have been written costs days of merge
conflicts and broken build graphs.

The folder structure must also preserve the **strict directory
confinement** property mandated by `CLAUDE.md` Rule 6: every build
cache, virtual environment, downloaded model, and generated artifact
stays inside the repository tree. No layout decision may implicitly
push state into `~/.cache`, `~/.bazel`, or any other global user
directory.

## Decision Drivers

- **D1. Clarity for new contributors.** Someone cloning the repo
  should be able to locate the C++ engine, the Go gateway, and the
  frontend within about ten seconds of looking at the root. The layout
  is a first-impression document.
- **D2. Bazel package compatibility.** Each component owns its
  `BUILD.bazel` file and can be built (`bazel build
  //engine_cpp/...`) independently of the others. The layout should
  map cleanly onto Bazel's package-per-directory conventions.
- **D3. Hexagonal architecture support.** `ARCHITECTURE.md` §5 and
  ADR-0002 Constraint 2 require provider interfaces for audio capture,
  transcript streams, auth, filesystem, and notifications. The folder
  layout must give those provider abstractions an obvious home so they
  are not scattered across the component tree.
- **D4. Directory confinement** (CLAUDE.md Rule 6). All caches,
  models, `.venv`, `node_modules`, and Bazel output must live inside
  the repo tree. The layout should make this natural, not fight it.
- **D5. Testability.** Golden audio fixtures, load-test scripts, and
  cross-component integration tests have a clear home independent of
  any single component's directory — they belong to the product, not
  to a component.
- **D6. Minimal depth.** The layout should not introduce nesting that
  has no navigational value. Every extra level of directory is a tax
  on every `cd` and every path string in a `BUILD.bazel` file.

## Considered Options

### Option A — Flat per-language (`src/cpp/`, `src/go/`, `src/ts/`)

Group by language at the top level, then by component underneath:

```
src/
├── cpp/
│   └── engine/
├── go/
│   └── gateway/
└── ts/
    └── frontend/
```

- Simple to explain.
- Familiar to developers coming from single-language mono-repositories.
- Rejected: polyglot monorepos with this layout become unnavigable
  once components proliferate. The cross-language **boundaries** —
  where the gRPC contract sits, where the frontend calls the gateway,
  where the engine consumes proto-generated C++ bindings — matter far
  more than the language of any given component. Burying components
  one level under their language obscures the boundaries.

### Option B — Per-component at root (`engine_cpp/`, `gateway_go/`, `frontend_web/`) ✅ chosen

Each component sits at the repository root, named for its role with
the implementation language as a suffix. Cross-cutting assets (proto
contracts, models, tests) also sit at the root.

- New contributors see the component map on the first `ls`.
- Each component's `BUILD.bazel` files live inside that component's
  subtree and can be referred to as `//engine_cpp/...`,
  `//gateway_go/...`, `//frontend_web/...`.
- The proto contracts have a neutral `proto/` home, emphasizing that
  they are a shared source of truth rather than owned by any single
  component.

### Option C — Nx-style `apps/` + `libs/` two-level nesting

```
apps/
├── engine_cpp/
├── gateway_go/
└── frontend_web/
libs/
├── shared_protobuf/
└── shared_utils/
```

- Familiar to teams coming from JavaScript mono-repositories
  (Nx, Turborepo, Rush).
- Rejected: adds navigation depth with no benefit when the component
  count is small (three components in MVP, plus a Phase 4 shell).
  The `apps/` vs `libs/` distinction is overfit for JavaScript-centric
  monorepos where the `libs/` directory holds dozens of shared
  packages. In a polyglot monorepo the equivalent "shared library"
  concept lives inside each language's native package system
  (C++ static libs under `engine_cpp/`, Go packages under
  `gateway_go/internal/`, TypeScript modules under `frontend_web/src/lib`),
  not in a top-level `libs/` directory.

## Decision Outcome

**We choose Option B (per-component at root).** The full tree is
documented below. Every entry is load-bearing — placeholders exist
where Phase 4+ content will land, but their names are fixed now so
references from other ADRs and documents remain stable.

```
aegis-core/
├── .github/
│   ├── workflows/                  # CI pipelines (Phase 0+)
│   └── CODEOWNERS
├── .bazelrc
├── .bazelversion
├── .gitignore
├── .pre-commit-config.yaml
├── ARCHITECTURE.md
├── BUILD.bazel                     # root Bazel build file
├── CLAUDE.md
├── CONTRIBUTING.md
├── LICENSE
├── MODULE.bazel                    # Bzlmod (replaces WORKSPACE)
├── README.md
├── ROADMAP.md
├── SECURITY.md
│
├── docs/
│   ├── adr/                        # architecture decision records
│   └── threat-model.md             # STRIDE threat model
│
├── proto/                          # language-neutral contracts — source of truth for C++/Go/TS
│   └── aegis/
│       └── v1/
│           ├── BUILD.bazel
│           └── aegis.proto
│
├── engine_cpp/                     # C++ inference engine
│   ├── BUILD.bazel
│   ├── cmd/engine/                 # main() entrypoint
│   ├── src/
│   │   ├── audio/                  # PCM buffer management, SensitiveBytes
│   │   ├── inference/              # whisper.cpp wrapper
│   │   ├── diarization/            # speaker diarization
│   │   ├── voiceprint/             # enrollment + cosine matcher
│   │   ├── rag/                    # in-process hnswlib query
│   │   ├── session/                # session manager + Pause/Resume state machine
│   │   ├── grpc/                   # gRPC service implementation
│   │   └── infra/                  # logging, telemetry, type wrappers
│   ├── include/                    # public headers
│   ├── tests/
│   │   ├── unit/
│   │   └── integration/
│   └── third_party/                # vendored whisper.cpp, grpc, etc. (Bazel http_archive SHA256)
│
├── gateway_go/                     # Go BFF gateway
│   ├── BUILD.bazel
│   ├── cmd/
│   │   ├── gateway/                # cloud-mode entrypoint
│   │   └── app_local/              # local-mode supervisor (ARCH §5)
│   ├── internal/
│   │   ├── session/                # session registry + fan-out
│   │   ├── webrtc/                 # Pion integration
│   │   ├── engine/                 # gRPC client to C++ engine
│   │   ├── auth/                   # Cognito (cloud) / dummy local
│   │   ├── token/                  # JWT issue & verify (ADR-0001)
│   │   ├── telemetry/              # OTel exporter + SpanProcessor (ADR-0005 R4)
│   │   ├── transport/              # gRPC-Web (cloud) + WebSocket (local, ADR-0007)
│   │   └── config/
│   ├── go.mod
│   └── go.sum
│
├── frontend_web/                   # React + Vite pure-web client (ADR-0003)
│   ├── BUILD.bazel
│   ├── package.json
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   ├── src/
│   │   ├── main.tsx
│   │   ├── pages/
│   │   │   ├── Host/
│   │   │   └── Viewer/
│   │   ├── providers/              # ADR-0002 Constraint 2 abstractions
│   │   │   ├── AudioCaptureProvider/
│   │   │   ├── TranscriptStreamProvider/
│   │   │   ├── AuthProvider/
│   │   │   ├── FileSystemProvider/
│   │   │   └── NotificationProvider/
│   │   ├── components/
│   │   ├── hooks/
│   │   ├── lib/
│   │   │   ├── webrtc/
│   │   │   ├── grpc-web/
│   │   │   ├── websocket/
│   │   │   └── protobuf/
│   │   └── styles/
│   └── tests/
│
├── shell_tauri/                    # Phase 4+ native shell (ADR-0002)
│   └── README.md                   # placeholder: "deferred to Phase 4"
│
├── models/                         # AI model artifacts
│   ├── manifest.json               # SHA256 + license + provenance (ARCH §10.1)
│   ├── README.md
│   └── .gitignore                  # exclude *.gguf / *.bin
│
├── deploy/                         # Phase 4 K8s manifests
│   └── k8s/
│       ├── base/
│       ├── overlays/
│       │   ├── staging/
│       │   └── production/
│       └── policies/               # Kyverno / Gatekeeper (ADR-0005 R6)
│
├── tools/                          # developer tooling
│   ├── bazelisk                    # Bazel version wrapper (CLAUDE.md Rule 6)
│   ├── scripts/
│   │   ├── download_models.sh      # SHA256-verified download
│   │   └── verify_model_hash.sh
│   └── ci/
│       ├── semgrep_rules/          # ADR-0005 R3/R4 custom rules
│       └── wer_threshold.txt
│
└── test/                           # cross-component tests
    ├── golden_audio/               # WER regression fixtures (ARCH §10.5)
    │   ├── en/
    │   ├── zh/
    │   ├── codeswitch/
    │   └── noise/
    ├── load/                       # k6 scripts
    └── fixtures/
```

### Why Option B

- **D1 wins decisively.** The first `ls` of the repository tells a new
  contributor exactly what lives where: `engine_cpp/`, `gateway_go/`,
  `frontend_web/`, `shell_tauri/`, `proto/`, `models/`, `deploy/`,
  `tools/`, `test/`. There is no rummaging through `src/*/something/`
  to find the Go code.
- **D2 is natural.** Each component gets its own Bazel package hierarchy
  rooted at its top-level directory. `bazel build //engine_cpp/...` and
  `bazel build //gateway_go/...` do exactly what a reader expects, and
  Bazel targets look structural rather than accidental.
- **D3 is satisfied.** The `frontend_web/src/providers/` directory is a
  dedicated home for the ADR-0002 Constraint 2 provider interfaces, and
  the Go gateway's `gateway_go/internal/transport/` directory mirrors
  the same pattern on the server side for the Cloud vs Local transport
  split (ADR-0007). A reader who knows the hexagonal model can find
  every adapter without guesswork.
- **D4 is natural.** `.bazel_cache/`, `.venv/`, and `node_modules/` all
  have obvious homes inside their owning component or at the repo root
  without any directory needing to push state outward. The
  `.gitignore` excludes them all.
- **D5 is clean.** The root-level `test/` directory holds fixtures that
  are shared across components — golden audio, load-test scripts,
  integration fixtures — so no individual component "owns" them and
  they cannot silently bit-rot inside a component whose maintainer
  stopped caring.
- **D6 is respected.** The deepest path in the tree is four or five
  levels from the root, and every level carries information. There is
  no `src/main/project/` ceremony.

### Why Not Option A (flat per-language)

- **D1 fails.** The top-level `src/` directory hides the component
  structure behind a language dimension that matters much less than
  the component dimension.
- **D3 fails.** Provider interfaces end up scattered: an
  `AudioCaptureProvider` in `src/ts/` has no obvious relationship to a
  parallel Go `TranscriptStreamProvider` in `src/go/`, even though they
  are two halves of the same hexagonal contract.
- **Bazel labels become less informative.** `//src/cpp/engine/...`
  reads as an implementation detail; `//engine_cpp/...` reads as a
  product structure.
- **Mixing cross-cutting content is awkward.** Where does `proto/` go?
  `src/proto/`? That hides it under a language prefix when proto is
  specifically language-neutral. Where do golden audio fixtures go?
  `src/` is wrong by definition.

### Why Not Option C (Nx-style `apps/` + `libs/`)

- **D6 fails.** Extra nesting depth (`apps/engine_cpp/cmd/engine/`)
  with no navigational value. Every path becomes longer for no reason.
- **Libs/ is the wrong mental model for polyglot.** The "shared
  library" concept in Nx assumes a single package manager (npm /
  yarn). In a polyglot monorepo each language has its own native
  package system, and forcing shared code into a top-level `libs/`
  directory breaks those native conventions (Go's `internal/`, C++
  header-only libs under `include/`, TS modules under
  `src/lib/`).
- **Small component count.** Aegis MVP has three components, plus a
  Phase 4+ placeholder. An `apps/` directory holding four things is
  pure ceremony.
- **The `proto/` problem again.** Proto contracts are neither apps
  nor libs; they are contracts. Nx's categorization has no natural
  home for them and they end up either duplicated or awkwardly
  shimmed into `libs/`.

## Consequences

### Positive

- **Navigation clarity**: a new contributor, or an AI agent spawned
  into a fresh clone, can locate every component in seconds without
  reading documentation first.
- **Bazel package ownership is structural**: each component owns its
  `BUILD.bazel` files and can be built in isolation. Cross-component
  dependencies are explicit in Bazel labels (`//proto/aegis/v1:aegis_cc_proto`,
  `//engine_cpp/src/audio:audio_lib`), which makes the dependency
  graph legible.
- **Provider abstractions have an obvious home**. Frontend providers
  live under `frontend_web/src/providers/`, gateway transport adapters
  live under `gateway_go/internal/transport/`, and auth providers are
  paired across both sides (`frontend_web/src/providers/AuthProvider/`
  and `gateway_go/internal/auth/`). The hexagonal architecture
  described in `ARCHITECTURE.md` §5 and ADR-0002 Constraint 2 maps
  directly onto the folder layout.
- **Cross-cutting assets are unambiguous**: `proto/` holds contracts,
  `models/` holds AI model artifacts, `test/golden_audio/` holds the
  WER regression suite referenced by `ARCHITECTURE.md` §10.5, and
  `tools/ci/` holds the CI-specific rules and thresholds.
- **CLAUDE.md Rule 6 confinement is preserved by construction**.
  `.bazel_cache/` (per `.bazelrc`), `.venv/` (if Python tooling is
  introduced), and `node_modules/` (per frontend package manager)
  all live inside the repo tree. The `.gitignore` entry and the
  Bazel `--output_user_root=./.bazel_cache` flag together guarantee
  no pollution of global user directories.

### Negative

- **`internal/` vs `src/` naming inconsistency** between Go and C++.
  Go uses `internal/` (a language-enforced visibility convention —
  packages under `internal/` cannot be imported from outside their
  parent module). C++ has no equivalent convention and uses `src/`
  for the same "implementation, not public API" role. A reader
  walking from `gateway_go/internal/` to `engine_cpp/src/` may
  wonder why the names differ; the answer is that each language is
  following its own native idiom, and forcing one name on both
  would hide Go's visibility semantics. Documented here so it is
  not mistaken for an oversight.
- **Phase 4+ placeholder directories**. `shell_tauri/` and `deploy/`
  exist as named placeholders before any content lands. A reader who
  clones the repo in Phase 2 will see empty-ish directories. This is
  an acceptable cost for having stable reference paths in ADRs and
  ARCHITECTURE sections that would otherwise say "to be created in
  a future directory whose name is not yet decided."
- **Root-level directory count is high** (12+ top-level folders once
  everything is present). This is the tradeoff for D1 — the first
  `ls` is informative but not short. We prefer informative over
  short.
- **Moving a component later would be expensive**. Once
  `//engine_cpp/...` labels propagate through `BUILD.bazel` files
  and CI configs, renaming the directory is a non-trivial refactor.
  The mitigation is to commit to this layout now and not revisit
  it without a new ADR.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers — defines the
  JWT middleware that lives under `gateway_go/internal/token/`.
- ADR-0002 Desktop Shell Technology — Constraint 2 provider
  abstractions live under `frontend_web/src/providers/` and are
  mirrored on the server side by `gateway_go/internal/transport/`.
- ADR-0003 Host Audio Capture Strategy — the
  `AudioCaptureProvider` interface lives at
  `frontend_web/src/providers/AudioCaptureProvider/`.
- ADR-0007 Local Mode LAN Topology — the Local mode supervisor
  entrypoint lives at `gateway_go/cmd/app_local/`, and the
  `TranscriptStreamProvider` WebSocket implementation lives under
  `frontend_web/src/providers/TranscriptStreamProvider/` alongside
  the gRPC-Web implementation.
- `ARCHITECTURE.md` §5 Dual-Mode Parity (Ports and Adapters) — the
  hexagonal architecture that drives the `internal/` / `providers/`
  layout.
- `CLAUDE.md` Rule 6 Strict Directory Confinement — the property
  this layout preserves by construction.
