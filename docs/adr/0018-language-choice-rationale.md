# ADR-0018: Language Choice per Component — polyglot for portfolio, all-Go for product

| Field    | Value                                                                   |
| -------- | ----------------------------------------------------------------------- |
| Status   | Accepted                                                                |
| Date     | 2026-04-14                                                              |
| Deciders | Project author                                                          |
| Context  | Phase 3; clarification triggered by the Opus-decode-on-engine refactor (ADR-0016) and the topology decision (ADR-0017), both of which surfaced "why C++ at all?" as a worth-defending question. |
| Related  | ADR-0008 (monorepo layout), ADR-0009 (C++ build + whisper.cpp), ADR-0016 (Opus decode on engine), ADR-0017 (gateway-engine topology) |

## Context

`aegis-core` runs three languages in production: **C++ for the
engine** (whisper.cpp + libopus + ASR pipeline), **Go for the gateway**
(BFF: WebRTC termination, session registry, JWT, fan-out), and
**TypeScript / React for the frontend**. A future Phase 4 Tauri shell
adds **Rust**. Each pair of languages incurs a contract-boundary cost
(proto wire format, cgo / FFI shims, Bazel build-graph integration,
two-language hiring, two-language debugging).

A reasonable product CTO reading this could ask: *"Why not just go
all-Go? whisper.cpp has Go bindings. Cognitive load and hiring pool
favor a single language. Horizontal scaling fixes the per-process
throughput gap. Stripe / GitHub / HashiCorp do this."* That position
is **correct for a product team**, and we should be honest about it
rather than pretend C++ is the universally right answer.

This ADR exists to make the choice between **portfolio rationale**
and **product rationale** explicit, so that:

1. Reviewers reading this repo know we considered both and chose
   deliberately, not by inertia.
2. A future contributor (or future-us, on a different project) can
   re-evaluate the same fork without re-discovering the analysis.
3. Interview conversations have a referenceable artifact when the
   "why polyglot?" question lands.

## Decision

**`aegis-core` ships the polyglot stack as documented in ADR-0008 +
ADR-0009 (C++ engine + Go gateway + TS frontend + future Rust Tauri).
For a real product team — different optimization function — the
recommendation would flip to all-Go with cgo-wrapped whisper.cpp +
libopus, scaled horizontally.**

The two recommendations are explicit:

### Recommendation A — `aegis-core` actual choice (portfolio-optimized)

- Engine: C++20 (`engine_cpp/`) — direct C++ to whisper.cpp + libopus,
  zero cgo crossings on the audio hot path, full SIMD/Metal/CUDA
  surface area.
- Gateway: Go 1.24 (`gateway_go/`) — hexagonal BFF, signal handling,
  WebRTC, fan-out. Stays pure Go; cgo limited to standard runtime
  needs.
- Frontend: React 19 + TypeScript 5.7 (`frontend_web/`) — typed
  end-to-end via proto-es bindings; same proto contract as the
  Go and C++ tiers.
- Future Tauri shell: Rust (Phase 4+). Reuses the React frontend
  via WebView per ADR-0002.

### Recommendation B — what I'd advise a product team (operational-cost-optimized)

- **Gateway + Engine both in Go**, with the engine being a Go binary
  that `cgo`-wraps whisper.cpp's C API and libopus. The same
  microservice contract (proto over gRPC) applies — engine still
  runs as a separate process, still N:N-scalable per ADR-0017.
- Frontend remains React + TS (no production case for changing
  this).
- No future Rust shell unless customer adoption proves a real
  need — Tauri-on-Rust is a learning investment that needs
  amortization.

## Rationale

### Why aegis-core chose Recommendation A

Three reasons in priority order:

1. **whisper.cpp is C++ regardless.** Even Recommendation B's
   "all-Go" path uses cgo to call into whisper.cpp's C API. The C++
   compiler dependency, the Bazel `rules_foreign_cc` glue, the
   per-frame FFI cost — all those exist in both recommendations.
   Recommendation A just doesn't bury the C++ binary under a
   thin Go cgo shim that adds ~200 ns / call overhead and obscures
   what's actually doing the work.

2. **Portfolio breadth signal.** The point of `aegis-core` is to
   demonstrate cross-stack architecture (Bazel polyglot monorepo,
   proto contracts that span three codegen targets, hexagonal
   ports made visible by language boundaries, hermetic toolchain
   for every language). Collapsing the engine to "thin Go shell
   wrapping a C++ binary" hides exactly the platform-engineering
   evidence that distinguishes this repo from a typical Go
   microservice scaffold.

3. **Cognitive cost of the "polyglot tax" is bounded by what's
   already required.** A reviewer cloning this repo already needs
   `bazelisk` (handles Go SDK + clang + protoc + buf + Node).
   Adding C++ as a *first-class* language costs zero additional
   prerequisites. The "Bazel ate the world" payoff is precisely
   that incremental polyglot is free at the toolchain layer.

### Why a product team should pick Recommendation B instead

1. **Hiring pool is wider for Go-only teams.** Mid-level Go
   engineers are abundant and cheap. C++ engineers who can
   navigate `whisper.cpp` + `ggml` + Bazel + macOS/Linux build
   variance are scarce and expensive. For a product company,
   replaceability matters more than per-process optimum.

2. **Operational cost of language diversity.** Two debuggers,
   two test framework cultures, two crash-dump formats, two
   profiling toolchains. Each one's cost is small; the sum
   accumulates. A Go-only team retains all of `pprof` + `go
   test` + `delve` and skips the C++ analogues entirely.

3. **Horizontal scaling is the right hammer for throughput.**
   The C-vs-Go cost of decoding one Opus frame is ~10 µs. At
   real-time audio (50 frames / second), that's 0.5 ms / second
   of CPU difference — invisible at the cost of one extra Pod.
   AWS will sell you a t4g.small for $13/month. Engineer time
   to maintain a polyglot stack costs more per hour than the
   compute the polyglot saves.

4. **C++ optimization payoff is per-Pod, but most workloads
   scale by adding Pods, not by maxing out one Pod.** The
   exception is single-machine on-device deployments (Aegis's
   Local mode is exactly this) — there, per-process throughput
   IS the user-experience ceiling. Local mode is the one place
   where Recommendation A pays its way operationally even
   ignoring the portfolio argument.

### Why the choice between A and B is not "one is wrong"

Both are reasonable answers to different optimization functions:

- **A** optimizes for *demonstrating capability across the stack
  and per-process throughput on the on-device deployment*.
- **B** optimizes for *team-velocity at scale, operational
  homogeneity, and accepting per-Pod inefficiency in exchange
  for cheap horizontal scaling*.

The honest framing in interview / review contexts is that we
**chose A for this repo because it's a portfolio piece**, and
*if asked* we'd recommend B for a real product team unless their
constraints (on-device, ultra-low-latency tail, etc.) push back.

## Consequences

### Positive

- A reviewer asking "why polyglot?" gets a direct, defensible
  answer with a clear caveat. The artifact is here in the repo,
  not hand-wave-improvised at interview time.
- Future-us re-considering this choice for another project has
  the analysis pre-baked. If the next project is product-shaped,
  ADR-0018 says "do B."
- The honest "I'd recommend B for a product team" answer signals
  architectural judgment maturity beyond "I picked the cool tech
  stack."

### Negative / costs

- A reviewer who only reads this ADR without context might
  conclude "the author admits polyglot is wrong, why are they
  using it?" The "for a portfolio piece" framing must stay
  load-bearing throughout the doc.
- Future contributors might see the recommendation-B advice and
  argue for migrating `aegis-core` itself. That would be
  inconsistent with the portfolio purpose. ADR-0018 settles
  the question for *this repo*; a fresh ADR would be needed to
  override the polyglot choice here.

## Alternatives Considered

### A1. Pure Go everywhere (no C++, no Rust, no FFI)

- **Pros**: Simplest possible stack.
- **Cons**: Forces a pure-Go ASR implementation. None of the
  pure-Go options (e.g. ports of whisper to Go) match
  whisper.cpp's per-Pod throughput, model coverage, or
  optimization breadth. Rejected because the underlying
  inference quality bar would fall noticeably.

### A2. Pure C++ everywhere (engine and gateway both C++)

- **Pros**: Maximum per-process throughput.
- **Cons**: Gateway is I/O-bound, not compute-bound. C++ doesn't
  buy throughput at the BFF layer; it just trades hiring-pool
  width for marginal latency that gRPC's runtime already
  dominates. Rejected because the BFF tier is a bad fit for
  C++'s strengths.

### A3. Rust for the engine

- **Pros**: Memory safety, modern toolchain.
- **Cons**: Rust whisper / ggml ports are immature compared to
  the C++ originals. We'd be coupling to a smaller, less-
  battle-tested codebase. Rejected for the same reason A1 is
  rejected — the underlying ASR runtime quality is what wins
  or loses on-device deployment, and whisper.cpp is the
  canonical implementation.

### A4. WASM-everywhere ("compile C++ engine to WASM, run in Go")

- **Pros**: Theoretically uniform deployment unit.
- **Cons**: WASM-compiled whisper loses the SIMD/Metal/CUDA
  fast paths that are the whole point of whisper.cpp. ~3-5×
  slower in our benchmarks. Rejected for being slower than
  the thing we're trying to optimize.

## Implementation checklist

This ADR is fundamentally retrospective — it documents the choice
already implemented across the repo. No code changes follow from
ADR-0018 itself. What the ADR enables:

- [x] Documented as ADR-0018 (this file).
- [ ] `README.md` ADR index gets a row pointing here.
- [ ] `docs/interview-notes.md` § "Decisions I can defend" gets
      a "why polyglot" entry that links here as the load-bearing
      defense artifact.
- [ ] `interview-prep.md` (gitignored personal cheat sheet) §
      Q&A list gets the "why not all-Go?" question with this
      ADR's framing as the canonical answer.
