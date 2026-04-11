# ADR-0009: C++ Build, Toolchain, and Dependency Integration

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

The C++ engine (`engine_cpp/` per ADR-0008) must be hermetically buildable
under Bazel, integrate `whisper.cpp`, serve gRPC bidirectional streams,
and link hardware acceleration backends on at least two target platforms
(macOS with Metal, Linux with CUDA or CPU). A handful of small but
interdependent decisions together define the **buildability story** for
Phase 1:

1. Which C++ language standard to target.
2. Which gRPC implementation to link against.
3. How to integrate `whisper.cpp` into the Bazel build graph.
4. How the build selects a hardware acceleration backend.

These decisions are collected in one ADR rather than split because they
are **tightly coupled** — each one constrains or simplifies the others,
and any future agent re-evaluating one must understand the interactions
with the other three.

## Decision Drivers

- **D1. Bazel hermeticity** — CLAUDE.md Rule 6 requires every toolchain,
  cache, dependency, and model to live inside the repository tree.
  Whatever we choose must not leak into the user's global system
  directories.
- **D2. "Clone and run minimal struggle"** — the MVP must build on a
  stock macOS laptop and a stock Linux dev box with one command and no
  prior installation steps beyond `git` and `python3`.
- **D3. Phase 1 velocity over optimal architecture** — we are shipping
  an MVP, not a long-lived research platform. Prefer stable, boring,
  and well-trodden over clever.
- **D4. Upstream whisper.cpp compatibility** — whisper.cpp is a moving
  target with frequent releases. The integration strategy must tolerate
  upstream churn without weekly BUILD-file archaeology.
- **D5. Cross-platform feasibility** — Linux and macOS must work in
  Phase 1. Windows is a Phase 4+ aspiration, not a Phase 1 blocker.
- **D6. Developer experience** — fast incremental rebuilds, clear error
  messages, and a single consistent command line across platforms.
- **D7. Security surface** — fewer moving parts means fewer supply chain
  attack vectors (ARCH §10.1 SBOM / SLSA / Cosign).

## Sub-decisions

This ADR contains four sub-decisions. Each has its own options and
rationale, and together they define the Phase 1 build.

---

### Sub-decision 1 — C++ Language Standard

#### Options

- **C++17** — conservative, universally supported, but missing
  `std::span`, concepts, `std::jthread`, `std::atomic_ref`, and the
  range-based improvements that clean up modern C++ code considerably.
- **C++20** ✅ — widely GA as of 2026 (Apple Clang 15+, GCC 10+, MSVC
  19.29+); brings `std::span`, concepts, ranges, `constexpr`
  improvements, and `std::jthread`. Some features (modules, coroutines)
  are still not uniformly solid, but we do not need them.
- **C++23** — bleeding edge, with `std::expected`, `std::print`,
  `std::mdspan`. Apple Clang lags noticeably, and several features are
  still flagged as "partial" on multiple toolchains.

#### Chosen: **C++20**

**Why**:

- `std::span<const int16_t>` is load-bearing for the `SensitiveBytes`
  type in ADR-0005 R3 (log formatter whitelist). The R3 guarantee —
  that audio PCM cannot be implicitly logged — relies on a type-safe
  non-owning view that C++17 cannot express natively.
- Concepts and ranges clean up the `audio/` and `inference/` module
  interfaces without runtime cost.
- By 2026, C++20 is the mainstream floor for new projects and all
  tooling (clang-tidy, clangd, CodeQL) supports it properly.
- C++23 would give us `std::expected` for free, but we already have
  `absl::StatusOr<T>` from the Abseil dependency we inherit via grpc-cpp
  (see Sub-decision 2), so this is not a meaningful upgrade for Aegis.

**Why not C++17**: blocks R3 enforceability with the same ergonomics.

**Why not C++23**: Apple Clang 15 support is uneven; `std::print` and
`std::mdspan` are not on the Phase 1 critical path. Re-evaluate in
Phase 4 or 5 once toolchains stabilize.

---

### Sub-decision 2 — gRPC Library

#### Options

- **grpc-cpp (`grpc++`)** ✅ — the official Google gRPC C++ library,
  with first-class Bazel support via `@com_github_grpc_grpc//:grpc++`
  and clean integration with `protoc`'s Bazel rules.
- **nghttp2 + manual HTTP/2** — roll a server atop `nghttp2` with a
  hand-written Protobuf encode/decode layer. Feasible but unnecessary
  yak-shaving for Phase 1.
- **grpc-web-proxy as sidecar, C++ speaks plain HTTP** — skip C++ gRPC
  entirely by letting a sidecar handle protocol conversion. Pushes
  complexity into the deployment topology and creates a failure-domain
  coupling across pods.

#### Chosen: **grpc-cpp**

**Why**:

- Bazel has first-class `@com_github_grpc_grpc//` support; adding the
  dep is one line in `MODULE.bazel`.
- Tight integration with `@com_google_protobuf//` and the
  `proto_library` / `cc_proto_library` / `cc_grpc_library` rule chain
  — our `proto/aegis/v1/aegis.proto` becomes a `cc_grpc_library` with
  no extra codegen infrastructure.
- Pulls in Abseil (`absl::Status`, `absl::StatusOr`, `absl::string_view`)
  as a natural side effect, which we use as our Phase 1 error type
  (see ADR-0010).
- Massive user base means nearly every C++ gRPC question has already
  been answered on Stack Overflow or in the upstream issue tracker.
- Bazel + grpc-cpp is a **known-good** combination; pairing with
  `rules_foreign_cc` (Sub-decision 3) is cleaner because all the
  first-party rules stay pure-Bazel.

**Why not nghttp2 / grpc-web-proxy**: both violate D3 (phase velocity).
grpc-web-proxy also violates D7 by adding a new pod-level attack
surface.

**Cost of grpc-cpp**:

- Build is heavy — grpc-cpp transitively pulls in BoringSSL, Abseil,
  upb, protobuf, and c-ares. Cold builds under `rules_foreign_cc` or
  pure-Bazel are 5–15 minutes on a modern laptop.
- Mitigated by Bazel remote cache once Phase 4 CI is online; local
  developers get incremental rebuilds in seconds after the initial
  cold build.

---

### Sub-decision 3 — whisper.cpp Integration Method

This is the single most load-bearing decision in this ADR because it
shapes the daily developer experience.

#### Options

- **(a) `http_archive` + `rules_foreign_cc`** ✅ — pin an upstream
  whisper.cpp release by URL and SHA256 via Bazel's `http_archive`,
  and invoke its native Makefile through
  `rules_foreign_cc`'s `make` rule from inside the Bazel sandbox.
  Upstream upgrades = bump a URL and SHA256.
- **(b) Pure Bazel `cc_library` rewrite** — manually enumerate
  whisper.cpp's `.c` and `.cpp` sources in a hand-maintained
  `BUILD.bazel`, so Bazel compiles every file itself. Maximum
  hermeticity and parallelism, but we own the build graph and must
  re-do it on every upstream version bump.
- **(c) Fork into `engine_cpp/third_party/whisper_cpp/`** — copy
  whisper.cpp's sources directly into our repo as a vendored
  directory. Total control but we own upstream integration forever.

#### Chosen: **(a) `http_archive` + `rules_foreign_cc`**

**Why**:

- **D3 velocity** — `rules_foreign_cc`'s `make` rule is a one-paragraph
  BUILD file. We point it at the `make` target, give it a list of
  output files, and Bazel treats the result as a normal `cc_library`.
- **D4 upstream tolerance** — when whisper.cpp cuts a new release, we
  change two lines (URL and SHA256) in `MODULE.bazel`. Option (b)
  would require us to enumerate source files every bump; option (c)
  would require us to `git subtree pull` and resolve conflicts every
  bump.
- **D1 hermeticity is preserved** — `rules_foreign_cc` invokes the
  native Makefile **inside the Bazel sandbox**, with Bazel-provided
  toolchain flags. The host's `/usr/bin/make` is not used; Bazel
  provides its own hermetic `make`.
- **Security** — the SHA256 pin on `http_archive` gives us ADR §10.1
  integrity for free. Supply-chain provenance is the same as any other
  Bazel external dependency.

**Cons of (a) and mitigations**:

| Con | Mitigation |
|---|---|
| First cold build is ~2–5 minutes slower than pure Bazel (Makefile-in-sandbox is less parallelized than pure Bazel) | Remote cache in Phase 4; local dev cares about incremental, not cold |
| `rules_foreign_cc` has its own cross-platform quirks on Windows | Phase 1 does not target Windows |
| Diagnostic output from `make` is harder to read than pure `cc_library` errors | Accept for Phase 1; revisit if it becomes a recurring developer pain |
| `make` inside sandbox can break if whisper.cpp's Makefile assumes host tools | Fixable by adding `build_tool_cache` entries; known pattern in `rules_foreign_cc` community |

**Escalation path**: if (a) proves unstable on a particular platform
during Phase 1 implementation, **escalate specifically that platform to
option (b)** (pure-Bazel `cc_library`) rather than migrating everything.
The switch is local to `engine_cpp/third_party/BUILD.bazel` and does
not cascade.

**Why not (b)**: violates D4 (every upstream bump is manual work). Good
fallback if (a) breaks repeatedly.

**Why not (c)**: violates D4 and also adds 50–100 MB to the repo clone
size, degrading D2.

---

### Sub-decision 4 — Hardware Acceleration Backend Selection

`whisper.cpp` supports multiple accelerators:

- **Metal** on macOS (Apple Silicon and Intel with AMX)
- **CUDA** on Linux with NVIDIA GPUs
- **CPU-only fallback** with AVX2/FMA for Linux and macOS Intel

The question is **how the Bazel build picks a backend**.

#### Options

- **Automatic detection at build time** — Bazel `select()` on platform
  constraints plus host inspection, choosing the backend that matches
  the host.
- **Manual `--config=metal|cuda|cpu` flag** ✅ — developer explicitly
  tells Bazel which backend to link. A default is set per host in
  `.bazelrc` via `--config=auto` mapping to the common case.
- **Runtime detection** — build all backends into the binary and
  detect available hardware at process startup. Maximum flexibility
  but significantly larger binary and more complex load-time logic.

#### Chosen: **Manual `--config=metal|cuda|cpu` flag for Phase 1**

**Why**:

- **Cross-platform compilation fragility** — automatic detection is
  feasible for native builds but breaks badly for **cross-compile**
  scenarios (developer on macOS targeting a Linux CUDA pod). Bazel's
  platform constraint mechanism can model this correctly, but the
  combinatorial explosion (host × target × backend) introduces far
  more ways for Phase 1 builds to fail silently than manual flags
  have ever produced.
- **Explicit is debuggable** — when a build fails, `--config=metal`
  localizes the cause to "Metal toolchain is broken." Automatic
  detection turns the same failure into "why is Bazel picking CPU
  when I have an M-series Mac?"
- **Matches D3** — one less moving part in Phase 1. Auto-detection can
  be added in Phase 2 after we have real experience with what works
  across platforms.
- **The user flagged this risk explicitly** — cross-platform
  auto-detection in Bazel has a history of producing "works on my
  machine" failures, confirming the conservative choice.

**Phase 2+ revisit**: if developer ergonomics demand it, add a
`--config=auto` convenience that inspects the host and picks one of
the three configs. The three explicit configs always remain available
as fallbacks.

**Why not runtime detection**: violates D3 (significantly larger
binary, multiple linked backends per build) and complicates ADR-0005
R6 (the deployment must pin the pod to a matching node — choosing
backend at runtime would require all engine pods to ship with all
backends and their dependencies, bloating the image).

---

## Decision Outcome — Summary

| Concern | Choice |
|---|---|
| C++ standard | **C++20** |
| gRPC library | **grpc-cpp (`grpc++`)** via `@com_github_grpc_grpc//` |
| whisper.cpp integration | **`http_archive` + `rules_foreign_cc` `make` rule** |
| Hardware backend selection | **Manual `--config=metal|cuda|cpu`** (Phase 1) |

## Implementation Notes

### `.bazelrc` skeleton

```bash
# Base flags applied to every build
build --output_user_root=./.bazel_cache     # CLAUDE.md Rule 6
build --cxxopt=-std=c++20
build --cxxopt=-Wall
build --cxxopt=-Wextra
build --cxxopt=-Werror
build --host_cxxopt=-std=c++20

# Backend selection — one of these should be chosen explicitly
build:metal --define=whisper_backend=metal
build:metal --copt=-DGGML_USE_METAL
build:metal --linkopt=-framework
build:metal --linkopt=Metal
build:metal --linkopt=-framework
build:metal --linkopt=MetalKit

build:cuda --define=whisper_backend=cuda
build:cuda --copt=-DGGML_USE_CUBLAS
build:cuda --action_env=CUDA_PATH

build:cpu --define=whisper_backend=cpu
build:cpu --copt=-DGGML_USE_AVX2
build:cpu --copt=-mavx2
build:cpu --copt=-mfma

# Developer convenience: host-appropriate defaults (opt-in)
build:auto_macos --config=metal
build:auto_linux --config=cuda

# Debug vs release
build:debug -c dbg
build:debug --copt=-DAEGIS_DEV_AUDIO_DUMP      # ADR-0005 R7 — dev only
build:release -c opt
build:release --strip=always
```

### `engine_cpp/third_party/whisper_cpp/BUILD.bazel` sketch

```python
load("@rules_foreign_cc//foreign_cc:defs.bzl", "make")

filegroup(
    name = "all_srcs",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)

make(
    name = "whisper_cpp",
    lib_source = ":all_srcs",
    out_static_libs = ["libwhisper.a", "libggml.a"],
    args = select({
        "//engine_cpp:backend_metal": ["WHISPER_METAL=1"],
        "//engine_cpp:backend_cuda":  ["WHISPER_CUBLAS=1"],
        "//engine_cpp:backend_cpu":   [],
    }),
    visibility = ["//engine_cpp:__subpackages__"],
)
```

### `engine_cpp/BUILD.bazel` backend config flags

```python
config_setting(
    name = "backend_metal",
    define_values = {"whisper_backend": "metal"},
)

config_setting(
    name = "backend_cuda",
    define_values = {"whisper_backend": "cuda"},
)

config_setting(
    name = "backend_cpu",
    define_values = {"whisper_backend": "cpu"},
)
```

Command-line usage:

```bash
# On macOS with Apple Silicon
bazel build //engine_cpp/... --config=metal

# On Linux with NVIDIA GPU
bazel build //engine_cpp/... --config=cuda

# Anywhere with CPU-only fallback
bazel build //engine_cpp/... --config=cpu
```

### Upstream whisper.cpp version bump procedure

1. Find the new upstream release tag on
   <https://github.com/ggerganov/whisper.cpp/releases>.
2. Compute SHA256 of the release tarball:
   `curl -L <url> | sha256sum`.
3. Update `MODULE.bazel`:
   ```python
   bazel_dep(name = "whisper_cpp", version = "<new_version>")
   # or if using http_archive directly:
   http_archive(
       name = "whisper_cpp",
       urls  = ["https://.../whisper.cpp/archive/<tag>.tar.gz"],
       sha256 = "<new_sha256>",
       strip_prefix = "whisper.cpp-<tag>",
       build_file = "//engine_cpp/third_party/whisper_cpp:BUILD.bazel",
   )
   ```
4. Run WER golden audio regression suite (ADR-0011) — any drift over
   threshold blocks the bump.
5. Run full CI including SBOM regeneration.
6. Commit under `build(deps): bump whisper.cpp to <version>`.

### CI matrix (Phase 4)

Until Phase 4, CI runs only the Phase 0 lint/scan jobs (see
`.github/workflows/ci-baseline.yml`). Once C++ builds exist in
Phase 1, add a matrix:

| Runner | Config | Purpose |
|---|---|---|
| `macos-14` (Apple Silicon) | `--config=metal` | Primary dev platform |
| `ubuntu-latest` | `--config=cuda` | Production Linux pod target |
| `ubuntu-latest` | `--config=cpu` | Fallback / Tier 3 pod target |

All three configs must pass before merge to `main`.

## Consequences

### Positive

- Single build command per platform; no developer-side config
  tinkering beyond `--config`.
- Modern C++20 ergonomics with broad toolchain support.
- Upstream whisper.cpp bumps are trivial (SHA256 + version bump).
- All dependencies flow through Bazel's hermetic graph — no host
  pollution.
- SHA256-pinned external deps give us free ADR §10.1 supply-chain
  provenance.
- `grpc-cpp` brings Abseil, which ADR-0010 uses for `absl::Status`.
- Backend selection is explicit and debuggable.

### Negative

- `rules_foreign_cc` cold builds are slower than pure Bazel `cc_library`.
  Mitigated by remote cache in Phase 4 and by the fact that local
  development runs incremental builds.
- Developers must remember `--config=metal` etc. Mitigated by
  `.bazelrc` `--config=auto_macos` / `--config=auto_linux` convenience
  shortcuts.
- grpc-cpp pulls a heavy dependency tree (BoringSSL, Abseil, upb,
  c-ares, protobuf). Mitigated by incremental builds and cache.
- C++20 subtle differences between Apple Clang 15, GCC 10+, and any
  future MSVC build. Mitigated by the Phase 4 CI matrix covering at
  least macOS and Linux.
- Cross-compile (developer on macOS targeting Linux CUDA) is not
  supported in Phase 1. If needed, use the Linux runner directly.

### Risks

- **whisper.cpp CMake migration**: upstream whisper.cpp has been
  considering moving from Makefile to CMake as the primary build
  system. If this happens, `rules_foreign_cc`'s `cmake` rule is a
  drop-in replacement for the `make` rule used here — migration is
  a ~30-minute edit to `engine_cpp/third_party/whisper_cpp/BUILD.bazel`.
- **Apple Clang C++20 regressions**: Apple Clang has shipped C++20
  regressions in the past. Mitigation: pin the macOS CI runner to a
  specific Xcode version and bump deliberately.
- **grpc-cpp major version churn**: grpc-cpp occasionally makes
  source-breaking changes in minor versions. Pin to a specific version
  in `MODULE.bazel` and bump only during planned maintenance.

## Related

- ADR-0005 Audio & Voiceprint Ephemeral Policy (R3 `SensitiveBytes`
  requires `std::span` → C++20)
- ADR-0008 Monorepo Folder Structure (`engine_cpp/`,
  `engine_cpp/third_party/`)
- ADR-0010 C++ Engine Runtime Architecture (uses `absl::Status`
  inherited from grpc-cpp)
- ADR-0011 WER Golden Audio Test Fixtures (runs on every whisper.cpp
  version bump)
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §10.1 Supply Chain Integrity
- `CLAUDE.md` Rule 6 Strict Directory Confinement
- [rules_foreign_cc documentation](https://github.com/bazelbuild/rules_foreign_cc)
- [grpc-cpp Bazel setup](https://github.com/grpc/grpc/tree/master/examples/cpp)
