# ADR-0021: Shared ggml runtime — one build, two consumers (whisper.cpp + llama.cpp)

| Field    | Value |
| -------- | ----- |
| Status   | Accepted |
| Date     | 2026-04-17 |
| Deciders | Project author |
| Context  | ADR-0020 mandates engine-owned embedding via bge-m3 GGUF. The only maintained C/C++ inference path for bge-m3 (XLMRoberta architecture) is llama.cpp. Adding llama.cpp alongside the existing whisper.cpp creates a ggml symbol collision (533+ duplicate symbols) that must be resolved structurally. |
| Related  | ADR-0009 (C++ build + whisper.cpp), ADR-0020 (engine owns inference), ADR-0010 (ModelBudget — bge-m3 adds ~438 MB) |

## Context

Three facts force this ADR:

1. **bge-m3 requires llama.cpp.** The XLMRoberta architecture
   (SentencePiece tokenizer, encoder-only BERT graph, CLS/mean
   pooling) is only supported in llama.cpp's `llama_encode()` +
   `llama_get_embeddings_seq()` path. The standalone `bert.cpp`
   project is abandoned ("merged into llama.cpp"). Raw ggml graph
   construction is possible but would take weeks to reimplement
   what llama.cpp already provides.

2. **whisper.cpp and llama.cpp both bundle ggml.** Both are from
   `ggml-org` and both vendor the ggml tensor library in their
   source trees. Linking both into one binary produces 533+
   duplicate symbol errors. This is a known upstream issue
   (llama.cpp #9267, whisper.cpp #1887, ggml #1148) with no
   upstream fix beyond the `USE_SYSTEM_GGML` escape hatch.

3. **Static linking is mandatory.** The engine is a single binary
   (ADR-0020 "one binary, one ggml runtime"). Dynamic linking
   with `RTLD_LOCAL` could theoretically isolate the two ggml
   copies, but it contradicts the static-link-everything Bazel
   hermetic build model and introduces runtime linker
   dependencies. In static linking, duplicate symbols are a hard
   linker error — there is no workaround.

## Decision

**Build ggml exactly once from the standalone `ggml-org/ggml`
repository. Both whisper.cpp and llama.cpp link against this
single ggml build via their `USE_SYSTEM_GGML=ON` CMake flags.**

### Version pin (initial)

All three repositories are pinned at ggml v0.9.8 compatibility:

| Component      | Version / Tag | ggml version | System ggml flag |
| -------------- | ------------- | ------------ | ---------------- |
| ggml standalone | **v0.9.8**   | 0.9.8        | N/A (is the source) |
| whisper.cpp    | **v1.8.4**    | 0.9.8        | `WHISPER_USE_SYSTEM_GGML=ON` |
| llama.cpp      | **b8595**     | 0.9.8        | `LLAMA_USE_SYSTEM_GGML=ON` |

### Bazel build topology

```
http_archive("ggml", v0.9.8)
  └─ cmake() → libggml.a, libggml-base.a, libggml-cpu.a

http_archive("whisper_cpp", v1.8.4)
  └─ cmake(WHISPER_USE_SYSTEM_GGML=ON, deps=[ggml])
     → libwhisper.a

http_archive("llama_cpp", b8595)
  └─ cmake(LLAMA_USE_SYSTEM_GGML=ON, deps=[ggml])
     → libllama.a

cc_binary("engine")
  └─ deps: whisper_cpp, llama_cpp, ggml (one copy of each symbol)
```

### Diamond dependency protection

These three deps form a **version-coupled triple**. Independent
upgrades break the build. Four protection mechanisms are required:

#### P1. MODULE.bazel coupling comment

```python
# ╔══════════════════════════════════════════════════════════════╗
# ║ VERSION-COUPLED TRIPLE — ggml / whisper.cpp / llama.cpp     ║
# ║                                                              ║
# ║ These three deps share one ggml runtime (ADR-0021).          ║
# ║ They MUST be upgraded together. Never bump one without       ║
# ║ verifying the other two are compatible.                      ║
# ║                                                              ║
# ║ Upgrade SOP: docs/CONTRIBUTING.md §Upgrading ggml triple    ║
# ╚══════════════════════════════════════════════════════════════╝
```

#### P2. Dependabot exclusion

Add to `.github/dependabot.yml`:

```yaml
# ggml / whisper.cpp / llama.cpp are a version-coupled triple
# (ADR-0021). Automated bumps would break the build because
# Dependabot bumps deps independently. Manual upgrade only.
ignore:
  - dependency-name: "ggml"
  - dependency-name: "whisper_cpp"
  - dependency-name: "llama_cpp"
```

#### P3. CI version-match check

A CI job that extracts the ggml version from all three deps and
fails if they diverge. Implementation: grep for
`GGML_VERSION_MAJOR/MINOR/PATCH` in each dep's source tree and
assert equality. Runs on every PR that touches `MODULE.bazel`.

#### P4. Upgrade SOP in CONTRIBUTING.md

Documented procedure:

1. Check ggml-org release notes for the latest ggml version.
2. Find a whisper.cpp tag that bundles or supports that ggml
   version (check `ggml/CMakeLists.txt` in the whisper.cpp
   tree for the `GGML_VERSION` string).
3. Find a llama.cpp tag that bundles or supports the same ggml
   version (check `ggml/CMakeLists.txt` in the llama.cpp tree).
4. Update all three `http_archive` entries in `MODULE.bazel`
   in a single commit.
5. Run the full test suite: `bazel test //engine_cpp/...`
6. PR title must include `deps(ggml-triple):` to signal the
   coupled upgrade.

Typical effort: ~30 minutes to find the compatible triple +
test. Not automatable by Dependabot because the compatibility
check requires cross-repo version inspection.

## Rationale

### Why not two separate ggml copies with symbol prefixing

`objcopy --prefix-symbols=whisper_ggml_` would prefix all ggml
symbols in whisper.cpp's copy, avoiding collision. Rejected:

- Every ggml API call site in whisper.cpp would need to use
  the prefixed names, requiring patches to whisper.cpp source.
- Every dep bump re-applies those patches — maintenance drag
  proportional to ggml's API surface (~200 public symbols).
- Fragile in Bazel: `objcopy` runs after compilation, so the
  C++ source still references unprefixed names; a two-pass
  build is needed.

### Why not separate processes

Running the embedder in a second process (IPC via Unix socket
or gRPC) eliminates the link collision entirely. Rejected per
ADR-0020: "one binary, one Metal/SIMD context, one memory
budget." A second process doubles the memory-mapped model
weight cost (no shared mmap across process boundaries) and
adds ~5 ms IPC latency per embed call.

### Why not dynamic linking with symbol isolation

`dlopen("libllama.so", RTLD_LOCAL)` would isolate llama.cpp's
ggml symbols. Rejected:

- Contradicts the Bazel hermetic static-link model.
- Introduces runtime linker dependencies (`ld.so` behavior).
- macOS's `dlopen` does not support `RTLD_LOCAL` for
  Mach-O — symbols leak into the flat namespace regardless.

### Why the standalone ggml repo over llama.cpp's bundled copy

Both whisper.cpp and llama.cpp have `USE_SYSTEM_GGML` flags
that expect `find_package(ggml)`. The standalone `ggml-org/ggml`
repo produces exactly that CMake package. Using llama.cpp's
bundled ggml would create a dependency of whisper.cpp on
llama.cpp, which is the wrong direction (they are peers, not
parent-child).

## Production alternative: microservice decomposition

This ADR's shared-ggml approach is the right answer for a
**portfolio repository** demonstrating single-binary model
co-location. A **production deployment** under different
constraints would likely choose differently.

If the engine were split into two microservices:

```
┌─────────────────────┐    ┌──────────────────────┐
│ ASR Service          │    │ Embedding Service     │
│ whisper.cpp + ggml   │    │ llama.cpp + ggml      │
│ (own version)        │    │ (own version)         │
└─────────┬───────────┘    └──────────┬────────────┘
          │ gRPC                      │ gRPC
          └───────────┬───────────────┘
                      ▼
              Go Gateway (BFF)
```

Each service owns its own ggml version. The diamond dependency
**vanishes** — upgrades are fully independent. The tradeoff:

| Dimension | Shared ggml (this ADR) | Microservice split |
| --------- | ---------------------- | ------------------ |
| Diamond dependency | P1–P4 mitigations | Eliminated |
| Upgrade coupling | 3 repos in lockstep | Independent |
| Memory | One ggml, shared Metal/SIMD context | Two copies (~2–3 MB each, negligible) |
| IPC latency | Zero (same process) | ~1–5 ms per call |
| Operational complexity | One binary, one deploy | Two services, two deploy pipelines |
| Portfolio signal | "I can co-locate models in one process" | "I can decompose services cleanly" |
| Local mode (16 GB ceiling) | One process, simpler | Two processes, slightly higher fixed overhead |

**Why this ADR chooses shared ggml anyway:**

1. ADR-0020's "one binary" thesis is a deliberate portfolio
   claim about model co-location and memory budgeting.
   Splitting into microservices would demonstrate a different
   (and more conventional) skill.
2. Local mode benefits from a single process — `app_local`
   spawns one child (the engine), not two.
3. The diamond dependency is manageable at portfolio scale
   (upgrades happen monthly, not hourly).

**When to reconsider:** If this repository transitions from
portfolio to production product, and the upgrade coordination
cost of P1–P4 exceeds the operational cost of running two
services, the microservice split becomes the better
engineering answer. That decision belongs in a future ADR
that revises ADR-0020's "one binary" constraint.

## Consequences

### Positive

- **Zero duplicate symbols.** One ggml, one address space, one
  Metal/SIMD context. Exactly what ADR-0020 prescribes.
- **Model co-location works.** whisper + bge-m3 (+ future LLM)
  share one ggml backend, one Metal context, one memory arena.
  Context switches between models are cache-friendly.
- **Upstream-blessed pattern.** whisper.cpp's `talk-llama`
  example does exactly this. The `USE_SYSTEM_GGML` flags exist
  for this purpose.
- **Container image stays small.** One copy of libggml
  (~2–3 MB static) instead of two.

### Negative

- **Diamond dependency.** Three repos must be upgraded in
  lockstep. Mitigated by P1–P4 above, but manual coordination
  is inherent.
- **ggml compatibility window is narrow.** A given ggml version
  is typically compatible with whisper.cpp and llama.cpp
  releases spanning ~2–3 weeks. Outside that window, API drift
  causes build failures. This forces frequent, small upgrades
  rather than infrequent large jumps.
- **Bazel `rules_foreign_cc` complexity.** Wiring
  `CMAKE_PREFIX_PATH` across three `cmake()` rules is
  non-trivial. The initial Bazel plumbing is ~1–2 days.
- **Dependabot cannot help.** The three deps must be excluded
  from automated bumping. This is a permanent maintenance
  obligation.

### Risks

- **ggml-org breaks `USE_SYSTEM_GGML`.** The flag is not
  heavily tested upstream (most users build ggml from the
  vendored copy). If a future release breaks the flag, we must
  file upstream or patch locally. Mitigation: the flag is a
  simple `find_package` + alias; patches are small.
- **ggml-org merges the repos.** If ggml-org eventually ships
  llama.cpp and whisper.cpp with a single shared ggml (their
  stated direction), this ADR becomes unnecessary and the
  three-dep pin simplifies to two or one. That is a positive
  outcome, not a risk.

## Implementation checklist

- [ ] Add `ggml-org/ggml` v0.9.8 as `http_archive` in
      `MODULE.bazel` with SHA256
- [ ] Add `ggml-org/llama.cpp` b8595 as `http_archive` in
      `MODULE.bazel` with SHA256
- [ ] Write `engine_cpp/third_party/ggml/ggml.BUILD` — cmake
      target producing `libggml.a` + friends
- [ ] Write `engine_cpp/third_party/llama_cpp/llama_cpp.BUILD`
      — cmake target with `LLAMA_USE_SYSTEM_GGML=ON`
- [ ] Update `engine_cpp/third_party/whisper_cpp/whisper_cpp.BUILD`
      — add `WHISPER_USE_SYSTEM_GGML=ON`, remove bundled ggml
      from the whisper build
- [ ] Verify `bazel build //engine_cpp/cmd/engine:engine`
      links without duplicate symbols
- [ ] Verify existing whisper tests still pass
- [ ] Add coupling comment block to `MODULE.bazel` (P1)
- [ ] Add Dependabot ignore rules (P2)
- [ ] Add CI ggml-version-match check (P3)
- [ ] Add upgrade SOP to `CONTRIBUTING.md` (P4)
- [ ] `GGMLEmbedder` implementation using llama.cpp C API
- [ ] Update `models/manifest.json` with bge-m3 Q4_K_M entry
      (source: `lm-kit/bge-m3-gguf`, 438 MB, SHA256)

## Related

- ADR-0009 C++ Build and Toolchain (whisper.cpp via Bazel)
- ADR-0020 Engine owns inference (one binary, unified runtime)
- ADR-0010 §Revision (ModelBudget — bge-m3 adds ~438 MB)
- whisper.cpp `talk-llama` example (canonical two-consumer
  reference)
- llama.cpp #9267 (533 duplicate ggml symbols)
- whisper.cpp #1887 (Xcode build conflict)
- ggml #1148 (shared library collision)
