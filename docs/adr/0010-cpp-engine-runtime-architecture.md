# ADR-0010: C++ Engine Runtime Architecture — Threading, Session, Resource Budget

- **Status**: Accepted (revised 2026-04-15 — see [§Revision](#revision-2026-04-15--resourcebudget-split-into-modelbudget--sessionbudget) for `ResourceBudget` split into `ModelBudget` + `SessionBudget`)
- **Date**: 2026-04-11 (original); 2026-04-15 (revision)
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

> **Revision note (2026-04-15)**: ADR-0020 lands bge-m3 GGUF and
> (eventually) an LLM into the engine process alongside whisper.cpp.
> The original `ResourceBudget` design conflates "model weights
> loaded once at startup" with "per-session working memory," which
> makes capacity math confusing once the engine owns more than one
> model. This ADR's Phase 1 decisions (sync-gRPC threading model,
> `absl::Status` error convention) are **unchanged**; the resource-
> budget sub-decision (§Sub-decision 2) is **extended** with the
> split described in the [Revision section](#revision-2026-04-15--resourcebudget-split-into-modelbudget--sessionbudget) at the end of this document. The original
> design intent is preserved — only the accounting structure
> changes.

## Context

The C++ engine is a single long-running process that serves multiple
concurrent gRPC bidirectional streams, each carrying one meeting's
audio PCM to whisper.cpp and producing speaker-labeled transcript
segments. It must also honor:

- The per-session ephemeral lifetime guarantee of ADR-0005 (audio PCM
  lives only in process RAM and vanishes on session end; voiceprint
  data is not processed at all per ADR-0012).
- The 16 GB local-mode memory ceiling of ARCHITECTURE.md §6.
- The Pause / Resume / End stream control semantics of ADR-0006.
- The clear "fail fast" property of ADR-0004 (a replica loss kills the
  session cleanly, no zombie state).

This ADR captures three tightly-related runtime decisions for Phase 1:

1. **Threading / concurrency model** — how gRPC streams are served.
2. **Resource budget** — how OOM is prevented structurally.
3. **Error handling convention** — how errors flow across module
   boundaries and out to clients.

These decisions shape every line of C++ code written in Phase 1. Getting
them down on paper before the first `cc_library` is written is load-
bearing for code coherence.

## Decision Drivers

- **D1. Phase 1 velocity** — pick the simplest model that demonstrably
  works for MVP capacity (target: up to ~25 concurrent sessions per
  engine pod, per the ARCH §6 budget calculation below).
- **D2. Session isolation** — ADR-0005 requires per-session lifetimes
  with clear destruction boundaries. The model must make session
  destruction trivially correct, not a whack-a-mole of reference
  counting.
- **D3. Hard memory ceiling** — 16 GB local ceiling (ARCH §6) is not a
  soft hint. An engine that OOM-kills under load is a crash, not a
  graceful degradation.
- **D4. Observable failure modes** — when overloaded, the engine must
  reject cleanly (`RESOURCE_EXHAUSTED`) rather than OOM-kill the whole
  process or thrash the OS.
- **D5. Upgradability** — Phase 2/3/4 should be able to evolve the
  concurrency model toward higher throughput without rewriting
  everything. Phase 1 code must not paint us into a corner.
- **D6. Debuggability** — when things go wrong, stack traces and logs
  must tell a coherent narrative. Complex async code with coroutines
  and callback chains is hostile to incident response.
- **D7. Consistency with grpc-cpp idioms** — chosen in ADR-0009.
  Whatever we pick should flow naturally from grpc-cpp's native API
  patterns.

## Sub-decisions

### Sub-decision 1 — Threading / Concurrency Model

#### Options

- **(i) Async gRPC + thread pool** — use grpc-cpp's async server API
  (`AsyncService`, completion queues). Each stream is a state machine
  driven by completion queue events. Inference work is dispatched to
  a dedicated thread pool. Complex but highest throughput.
- **(ii) Sync gRPC, 1 session = 1 thread** ✅ — use grpc-cpp's sync
  server API. Each gRPC bidirectional stream runs on its own thread
  (grpc-cpp manages the thread). The Session object's lifetime ==
  the thread's lifetime. The audio ring buffer and all transient
  working tensors live on the thread's stack / heap. When the stream
  ends, the thread exits, the session destructs, and all resources
  are freed deterministically.
- **(iii) Sync gRPC + MPSC queue + worker pool** — gRPC thread
  receives `IngestMessage`s and pushes them onto a multi-producer /
  single-consumer queue. A small pool of worker threads pops items,
  does inference, and writes transcript segments back to per-session
  output channels. Compromise between (i) and (ii).

#### Chosen: **(ii) Sync gRPC, 1 session = 1 thread**

**Why**:

- **D1 velocity**: the simplest possible model. A `SessionHandler`
  method runs top-to-bottom on a single thread, owns the `Session`
  object, and returns when the stream closes.
- **D2 session isolation**: lifetime reasoning is trivially local.
  `Session` is a stack or scoped object; its destructor runs when the
  stream ends; the destructor frees the audio ring buffer and any
  derived working state. No reference counting, no shared state
  between sessions.
- **D6 debuggability**: one thread per session means stack traces are
  narrative — "Session 42 is doing inference at line X of whisper.cpp".
  Async / coroutine stack traces are 30% true stack and 70% runtime
  scheduling artifacts.
- **D7 consistency**: grpc-cpp's sync API is the default and the one
  most examples use. Idiomatic code, fewer landmines.
- Naturally satisfies ADR-0005 R1: session RAM does not survive the
  stream because the thread does not survive the stream.

**Cons and mitigations**:

| Con | Mitigation |
|---|---|
| Thread count = concurrent sessions. At 1000 concurrent sessions, 1000 OS threads. | Phase 1 target is ~25 sessions per pod (see §6 below). Thread count is not load-bearing until Phase 4 capacity tests prove otherwise. |
| grpc-cpp sync API cannot easily share a single thread across many streams. | This is the opposite of what we want — we **want** per-stream isolation. |
| Thread-per-connection is considered "old-school" in 2026. | "Old-school" is the same as "known to work." D3 velocity dominates. |

#### Phase 2+ Upgrade Path to Model (iii)

When load tests show thread count hurting p99 latency (likely around
200+ concurrent sessions per engine pod), escalate:

1. Introduce an `InferenceQueue` abstraction. `Session` pushes PCM
   chunks to the queue instead of calling whisper.cpp directly.
2. Spin up N worker threads (where N is small, e.g., 2× GPU count).
3. Workers pop chunks, run inference, write transcript back via a
   per-session output channel (MPMC queue or similar).
4. `Session` object becomes a "dehydrated" state holder — it stops
   owning a dedicated thread.
5. gRPC boundary does not change. Client-visible behavior is
   identical.

The upgrade is fully local to `engine_cpp/src/session/` and
`engine_cpp/src/inference/`. Phase 1 code that obeys the
"`Session` owns its thread" contract does not have to change its
external interface.

#### Why Not (i) Async

- Violates **D1** (multi-week learning curve for async gRPC).
- Violates **D6** (async stack traces are hostile to debugging).
- Premature optimization — we do not have Phase 4 load data yet.
- grpc-cpp async API is notoriously finicky; half of the upstream
  issues about grpc-cpp are about async completion queue behavior.

#### Why Not (iii) for Phase 1

- The right model eventually, but it introduces a queue abstraction
  and worker lifecycle that is more complex than (ii) without
  throughput benefit at Phase 1 capacity targets.
- Add in Phase 2/3 when load test data says it matters.

---

### Sub-decision 2 — Resource Budget and OOM Protection

#### The Budget Problem

`ARCHITECTURE.md` §6 specifies a hard 16 GB local-mode ceiling across
the entire system (OS + browser + frontend + Go gateway + C++ engine).
Assume 8 GB for everything outside the engine. That leaves the engine
with **~8 GB** in the worst case (Apple Silicon M-series base tier
typically has 16 GB unified memory).

Fixed model overhead (revised per ADR-0012 — embedder removed because
Aegis no longer performs voiceprint matching):

| Component | RAM |
|---|---|
| whisper large-v3-turbo, Q4 quantization | ~1.5 GB |
| Speaker diarization model (anonymous clustering only) | ~1.0 GB |
| **Fixed total** | **~2.5 GB** |

Optional additions (Phase 5+ only, not MVP):

| Component | RAM |
|---|---|
| Llama-3-8B-Instruct Q4_K_M (for RAG generative answers) | ~4.8 GB |

Working budget per session (revised per ADR-0012 — voiceprint
embedding storage removed):

| Component | RAM |
|---|---|
| Audio ring buffer (30 seconds @ 16 kHz mono, 16-bit PCM) | ~1 MB |
| whisper.cpp working tensors | ~50–150 MB |
| Speaker diarization working state | ~20 MB |
| RAG query scratch space | ~10 MB |
| **Per-session total** | **~80–180 MB** (budget estimate 150 MB) |

**Two numbers, two purposes**: the per-session estimate (~150 MB)
is the expected median usage based on the component table above.
The per-session **reservation** (200 MB) is the conservative value
that `ResourceBudget::Reserve()` actually uses (see Design below).
Capacity math must use the **reservation** value, not the estimate,
because the budget is a hard ceiling and over-commit is forbidden.

#### Local Mode Capacity (16 GB ceiling)

With ~5.5 GB remaining after fixed costs, the arithmetic is:

```
Local mode max sessions
= (engine_budget - fixed) / reservation
= (8000 - 2500) / 200
≈ 27 sessions
```

This is the **hard cap for a 16 GB Apple Silicon machine** (ARCH §6).
It assumes no Llama-3. With Llama, subtract ~4.8 GB and the cap drops
to ~3 sessions (Llama is a Phase 5+ option, not MVP).

Compared to the pre-ADR-0012 design with voiceprint matching (~25
sessions on 250 MB reservation), removing voiceprint matching still
provides a material capacity improvement on the same hardware.

#### Cloud Mode Capacity (scaled by pod size)

In Cloud mode, the engine pod's memory is configurable via K8s
resource requests/limits. The formula is the same:

```
Cloud mode max sessions
= (pod_memory_limit - fixed_overhead) / reservation
```

Examples:

| Pod memory limit | Fixed (no Llama) | Available | Max sessions |
|---|---|---|---|
| 8 GB | 2.5 GB | 5.5 GB | 27 |
| 16 GB | 2.5 GB | 13.5 GB | 67 |
| 32 GB | 2.5 GB | 29.5 GB | 147 |
| 8 GB (with Llama) | 7.3 GB | 0.7 GB | 3 |
| 16 GB (with Llama) | 7.3 GB | 8.7 GB | 43 |

The `ResourceBudget` total is set at engine startup from the pod's
declared memory limit (or from a CLI flag in Local mode). This is
a Phase 1 startup-time constant, not a runtime-tunable value.

#### Design — `ResourceBudget`

A `ResourceBudget` singleton (or dependency-injected service) tracks
allocated bytes and gates session creation:

```cpp
class ResourceBudget {
 public:
  explicit ResourceBudget(std::size_t total_bytes);

  // Try to reserve `bytes`. Returns ok on success, RESOURCE_EXHAUSTED
  // if the reservation would exceed the budget.
  absl::Status Reserve(std::size_t bytes);

  // Release a prior reservation. Must be paired 1:1 with Reserve().
  void Release(std::size_t bytes);

  // Observability — exported as a metric.
  std::size_t BytesUsed() const;
  std::size_t BytesAvailable() const;

 private:
  const std::size_t total_bytes_;
  std::atomic<std::size_t> bytes_used_;
};
```

Usage contract:

- `SessionFactory::CreateSession()` estimates the per-session budget
  (200 MB default; configurable per model) and calls
  `ResourceBudget::Reserve(estimate)`. On failure, the incoming gRPC
  stream is rejected with
  `absl::Status(absl::StatusCode::kResourceExhausted, ...)`, which
  grpc-cpp translates to `grpc::StatusCode::RESOURCE_EXHAUSTED` on
  the wire.
- `Session::~Session()` calls `ResourceBudget::Release(estimate)` in
  its destructor. This runs on the same thread that owns the session
  (per Sub-decision 1), so reservation/release is naturally paired.
- `ResourceBudget` is **thread-safe** via `std::atomic`.
- `ResourceBudget` is **observable** — a Prometheus metric
  `aegis_engine_budget_bytes_used` is exported and scraped per ARCH
  §10.6.

#### Hard Rules

- **No over-commitment**: there is no "soft" limit or "admit one
  more and hope for the best." If `Reserve` would exceed the ceiling,
  the session is rejected **before** the engine allocates anything.
- **No best-effort release**: release is unconditional in the
  destructor. If a Session object is created, it is destroyed.
- **No reserve elsewhere**: only `Session` allocates budget. Other
  components (model loader, telemetry) use the ambient process heap;
  they are part of the fixed overhead, not per-session budget.
- **Reservations are conservative**: initial 200 MB default is a
  conservative upper bound. Phase 2 will tune via real data from CI
  load tests.

#### What OOM Protection Buys

| Failure mode | Before `ResourceBudget` | After |
|---|---|---|
| Load spike → OOM killer terminates engine | Whole pod dies, all sessions lost | New sessions rejected with `RESOURCE_EXHAUSTED`; existing sessions continue |
| Single session with pathological audio leaks | Eventual OOM | Same; `ResourceBudget` doesn't catch in-session growth |
| Llama-3 accidentally enabled | Silent bloat, eventual crash | Fixed overhead calculation fails at startup, refuses to boot |

The second row is a known gap — `ResourceBudget` gates **creation**,
not **usage**. A session that misbehaves after creation can still
leak. Mitigation: Phase 2 adds per-session hard RAM cap via `rlimit`
or cgroups. For Phase 1, trust whisper.cpp not to misbehave.

---

### Sub-decision 3 — Error Handling Convention

#### Options

- **`absl::Status` / `absl::StatusOr<T>`** ✅ — Google's recommended
  error type for C++; inherited from grpc-cpp's Abseil dependency.
  Converts cleanly to `grpc::Status` at the gRPC boundary.
- **C++ exceptions** — traditional C++ error handling. Banned in
  grpc-cpp's own codebase and most C++ gRPC examples; considered
  harmful for async / multithreaded code.
- **Return codes (`int` / `enum`)** — pre-Abseil idiom; does not
  compose with `StatusOr<T>`.

#### Chosen: **`absl::Status` / `absl::StatusOr<T>`**

**Why**:

- grpc-cpp pulls Abseil in transitively (ADR-0009 Sub-decision 2), so
  we pay no new dependency cost.
- `absl::Status` → `grpc::Status` conversion is one line; the error
  flows naturally from internal modules out to the RPC boundary.
- Explicit error handling matches the safety-critical nature of audio
  / biometric processing. Exception-based code has a history of
  leaking invariants on the unwinding path, and our ADR-0005
  invariants are **exactly** the kind of thing we cannot afford to
  leak.
- Modern C++ style guides (Google, LLVM, Abseil) recommend status
  types over exceptions.
- Phase 3 frontend uses TypeScript error values, and Phase 2 Go
  gateway uses Go errors — `absl::Status` matches the overall
  explicit-error style of the system.

#### Hard Rules

- **No C++ exceptions in production code paths.** Compile with
  `-fno-exceptions` where practical. Test code may use exceptions
  for `gtest` assertions — `-fno-exceptions` is applied only to
  `cc_library` targets under `engine_cpp/src/`, not to
  `engine_cpp/tests/`.
- **No string-based error construction.** Error codes come from
  `absl::StatusCode`; error messages are built via
  `absl::StrCat` with structured fields.
- **No silent conversion to boolean.** Code must explicitly match
  on the status; `if (status)` is not allowed where the status is
  a real error (use `.ok()`).
- **gRPC boundary is the only conversion point.** Internal modules
  return `absl::Status`; the RPC handler converts exactly once at
  the outermost layer via the standard grpc-cpp helper.

#### Example

```cpp
// internal module — returns absl::Status
absl::StatusOr<Transcript> InferenceEngine::Transcribe(
    absl::Span<const int16_t> pcm) {
  if (pcm.empty()) {
    return absl::InvalidArgumentError("empty pcm");
  }
  auto whisper_out = whisper_full(ctx_, params_, pcm.data(), pcm.size());
  if (whisper_out != 0) {
    return absl::InternalError(
        absl::StrCat("whisper_full failed: code=", whisper_out));
  }
  return Transcript{ExtractSegments(ctx_)};
}

// gRPC handler — converts at the boundary
grpc::Status AegisEngineServiceImpl::StreamTranscribe(
    grpc::ServerContext* ctx,
    grpc::ServerReaderWriter<EgressMessage, IngestMessage>* stream) {
  absl::Status status = session_->Run(stream);
  if (!status.ok()) {
    return grpc::Status(static_cast<grpc::StatusCode>(status.code()),
                        std::string(status.message()));
  }
  return grpc::Status::OK;
}
```

---

## Decision Outcome — Summary

| Concern | Choice |
|---|---|
| Threading model | **Sync gRPC, 1 session = 1 thread** |
| Concurrency upgrade path | Model (iii) MPSC queue + worker pool in Phase 2+ if load data demands it |
| Resource budget | **`ResourceBudget` class with atomic counters, fail-fast on reserve** |
| Per-session estimate (Phase 1) | **200 MB** (conservative) |
| Error handling | **`absl::Status` / `absl::StatusOr<T>`, exceptions banned** |
| gRPC boundary conversion | **Single conversion point in the RPC handler** |

## Consequences

### Positive

- Phase 1 code is maximally clear and debuggable.
- Lifetime reasoning is local to a thread — session creation, use,
  and destruction all happen on one thread.
- ADR-0005 R1 (audio in process RAM, session-scoped) is trivially
  correct: the thread is the session is the RAM scope.
- OOM is replaced by clean `RESOURCE_EXHAUSTED` rejection, which the
  Go Gateway can surface to the frontend as "engine busy, please
  retry shortly."
- `absl::Status` flows cleanly from internal modules to RPC
  boundaries without exception handling complications.
- grpc-cpp's most idiomatic API pattern, matching its examples and
  documentation.

### Negative

- **Not the highest-throughput architecture.** Thread-per-session
  hits diminishing returns around 200+ concurrent sessions. The
  upgrade path to Model (iii) is documented but not implemented.
- **Per-session budget estimate is pessimistic.** First iteration
  may reject sessions that would have fit. Tune with data in Phase
  2.
- **No mid-session OOM protection.** `ResourceBudget` gates
  creation, not growth. A pathological audio input could cause
  in-session memory growth that escapes budgeting. Phase 2+
  mitigation: per-pod rlimit or cgroups.
- **Hard exception ban complicates integrating third-party C++
  libraries that throw.** We must wrap them with catch-all
  boundaries that convert `std::exception` to `absl::InternalError`.

### Risks

- **grpc-cpp thread count hitting OS limits.** macOS default thread
  limit is 4096 per process; Linux is usually 30000+. At Phase 1
  capacity targets we are nowhere near this, but it is worth
  monitoring in load tests.
- **`ResourceBudget` estimate drift.** Upstream whisper.cpp or
  diarization model may grow in memory footprint over time. Phase 2
  CI load tests should assert actual peak RAM stays within the
  declared budget; if it drifts, raise the per-session estimate and
  lower the session cap.
- **Abseil ABI churn.** Abseil has made ABI-breaking changes in
  minor versions. Pin the Abseil version in `MODULE.bazel` and bump
  deliberately.

## Open Implementation Questions (Phase 1 / 2)

Not blocking this ADR; noted so the Phase 1 engineer does not
rediscover them:

- **Graceful shutdown at process level**: what happens when the
  engine pod receives SIGTERM? Current plan: reject new streams,
  let existing streams run to completion within
  `terminationGracePeriodSeconds` (matched to ADR-0006's 14400 s
  for Go Gateway, aligning with `session_max_lifetime`).
- **Metrics naming**: align
  `aegis_engine_budget_bytes_used`,
  `aegis_engine_sessions_active`,
  `aegis_engine_sessions_rejected_total{reason="budget"}`
  with the overall domain metric naming convention from ARCH §10.6.
- **Per-model `estimated_bytes` source of truth**: hardcoded
  constant for Phase 1; move to the `manifest.json` in `/models/`
  in Phase 2 so adding a new model does not require a code change.
  The `estimated_ram_bytes` field in `manifest.json` is already
  defined for this purpose.
- **AskRAG threading constraint** (forward reference from
  ADR-0012 "Future Outlook"): if an explicit query RPC is re-added
  in Phase 5+, its implementation **MUST run on a dedicated worker
  pool**, not on the session thread. Injecting RAG+LLM work into
  the session thread would contend with whisper inference for
  CPU/GPU cycles, causing audible latency spikes. The upgrade path
  to model (iii) MPSC queue + worker pool (Sub-decision 1 above)
  is the natural home for this. See ADR-0012 for full rationale.
- **`rlimit` vs cgroups for Phase 2 mid-session cap**: pod-level
  cgroups are simpler in K8s; explore once we have CI load-test
  data.

## Revision: 2026-04-15 — ResourceBudget split into ModelBudget + SessionBudget

### Why this revision exists

The original 2026-04-11 design treats "fixed model overhead"
(whisper + diarization = ~2.5 GB) as a **subtractive constant** in
capacity math and uses a single `ResourceBudget` to track
per-session reservations. That was fine when the engine owned
exactly one inference model (whisper.cpp); it becomes awkward the
moment ADR-0020 lands bge-m3 GGUF (~400 MB) next to it, and will
break down entirely when a future LLM (Qwen / Llama Q4_K_M, ~4–8
GB) joins.

Three specific pressures from ADR-0020:

1. **More than one model lives in process.** whisper + bge-m3 today,
   LLM tomorrow. Each has a distinct lifetime: loaded at engine
   startup, never freed. A single `ResourceBudget::Reserve` /
   `Release` pair assumes symmetric ownership, which these don't
   have.
2. **Model weights are shared across all sessions**, not reserved
   per-session. Charging per-session for memory that exists
   regardless of session count double-counts the budget.
3. **Observability clarity.** `aegis_engine_budget_bytes_used` as a
   single number hides whether pressure is coming from model bloat
   (fix: change quantization / swap model) or session pressure
   (fix: scale out). Splitting the metric tells the on-call engineer
   which lever to pull.

### The split

**`ModelBudget`** — process-global. Populated at engine startup as
each model loads and registers its footprint. Immutable thereafter
(no `Release` — model weights live for the process lifetime).

**`SessionBudget`** — per-engine-instance, sized as
`(pod_memory_limit - ModelBudget::TotalUsedBytes())` at engine
startup, then consumed session-by-session via the existing
`Reserve`/`Release` contract.

Both are observable via separate Prometheus metrics
(`aegis_engine_model_bytes_used{model="whisper"|"bge-m3"|"llm"}`,
`aegis_engine_session_bytes_used`).

### API shape

```cpp
// engine_cpp/src/session/model_budget.h — new
class ModelBudget {
 public:
  // Called by each model loader at startup. Thread-safe; the
  // accumulated total is read once after all models have registered.
  // No matching Release — model weights live for the process
  // lifetime.
  static void Register(std::string_view model_name, std::size_t bytes);

  // Total model footprint. Read after all models have loaded;
  // passed into the SessionBudget constructor.
  static std::size_t TotalUsedBytes();

  // Per-model breakdown for observability.
  static std::vector<std::pair<std::string, std::size_t>> Breakdown();
};

// engine_cpp/src/session/session_budget.h — renamed from ResourceBudget
class SessionBudget {
 public:
  // total_bytes should be (pod_memory_limit - ModelBudget::TotalUsedBytes())
  // computed at engine startup AFTER all models have registered.
  explicit SessionBudget(std::size_t total_bytes);

  // Same Reserve/Release semantics as the original ResourceBudget.
  absl::Status Reserve(std::size_t bytes);
  void Release(std::size_t bytes);

  std::size_t BytesUsed() const;
  std::size_t BytesAvailable() const;

 private:
  const std::size_t total_bytes_;
  std::atomic<std::size_t> bytes_used_;
};
```

Startup order in `engine_cpp/cmd/engine/main.cc`:

```cpp
int main() {
  const std::size_t pod_limit = ReadPodMemoryLimit();  // CLI flag or K8s env

  // Each loader registers with ModelBudget as it loads weights.
  auto whisper = WhisperEngine::Create(model_path);       // ~75 MB tiny.en
  auto embedder = GGMLEmbedder::Create(bge_m3_gguf_path); // ~400 MB Q4_K_M

  // Now all models are loaded; size SessionBudget from what's left.
  const std::size_t session_pool = pod_limit - ModelBudget::TotalUsedBytes();
  SessionBudget session_budget(session_pool);

  // Session factory uses session_budget for per-session reserves.
  RunServer(session_budget, ...);
}
```

### Revised capacity math (Phase 3b with ADR-0020 models landed)

Model budget with Phase 3b scope (whisper tiny.en for demo,
bge-m3 Q4_K_M for RAG; no LLM yet):

| Model                          | RAM    |
| ------------------------------ | ------ |
| whisper tiny.en (FP16)         | ~75 MB |
| bge-m3 Q4_K_M GGUF             | ~400 MB |
| **Phase 3b ModelBudget total** | **~475 MB** |

With a future LLM (Phase 5+ scope, illustrative):

| Model                    | RAM    |
| ------------------------ | ------ |
| whisper large-v3-turbo Q4 | ~1.5 GB |
| bge-m3 Q4_K_M            | ~400 MB |
| Llama-3-8B-Instruct Q4_K_M | ~4.8 GB |
| **ModelBudget total**    | **~6.7 GB** |

Revised session cap table (SessionBudget reservation unchanged at
200 MB per session):

| Pod memory | ModelBudget | SessionBudget pool | Max sessions |
| ---------- | ----------- | ------------------ | ------------ |
| 8 GB       | 475 MB (Phase 3b) | ~7.5 GB    | ~37         |
| 16 GB      | 475 MB (Phase 3b) | ~15.5 GB   | ~77         |
| 16 GB      | 6.7 GB (+LLM)     | ~9.3 GB    | ~46         |
| 32 GB      | 6.7 GB (+LLM)     | ~25.3 GB   | ~126        |

The local-mode 16 GB Apple Silicon ceiling (8 GB engine pod
effective) now supports ~37 concurrent sessions in Phase 3b —
slightly higher than the original 27 because the Phase 3b
ModelBudget (bge-m3-only) is smaller than the original's assumed
whisper-large + diarization overhead (~2.5 GB). The original
numbers in [§Local Mode Capacity](#local-mode-capacity-16-gb-ceiling) describe
the Phase 5+ / full-model scenario, not Phase 3b.

### Observability

Original single metric:

```
aegis_engine_budget_bytes_used  {gauge}
```

Revised into two metrics:

```
aegis_engine_model_bytes_used    {gauge, label: model="whisper"|"bge-m3"|"llm"}
aegis_engine_session_bytes_used  {gauge}
aegis_engine_sessions_active     {gauge}    # unchanged
aegis_engine_sessions_rejected_total{reason="session_budget"}  # was "budget"
```

The rejection label specificity matters: a future
`reason="model_budget"` (for the startup-time "models don't fit in
pod" check) is structurally different from mid-run session
rejection.

### What's explicitly unchanged

- **Threading model** (§Sub-decision 1): still sync gRPC, one
  session = one thread. Nothing about the split affects how
  streams are served.
- **Error handling convention** (§Sub-decision 3): still
  `absl::Status` / `absl::StatusOr<T>`, exceptions banned. Both
  `ModelBudget::Register` and `SessionBudget::Reserve` return
  / take `absl::Status`.
- **Per-session reservation default**: still 200 MB. The split
  doesn't touch the per-session estimate; it only changes
  what's counted in the pool against which reservations are
  checked.
- **Over-commitment rule**: still "if reserve would exceed the
  pool, reject immediately with `RESOURCE_EXHAUSTED`." Applies
  only to `SessionBudget`. `ModelBudget::Register` calls at
  startup are NOT allowed to exceed `pod_limit`; if they would,
  the engine fails to boot with a clear error (per Hard Rules
  below).

### Hard rules (additional to §Sub-decision 2)

- **Models register before `SessionBudget` is constructed.**
  This is enforced by construction order in `main.cc`. Any model
  loaded later (hot-reload, Phase 5+ dynamic LLM selection) needs
  its own revision.
- **ModelBudget is startup-time.** No `Release`, no hot-reload,
  no shrinking during the process lifetime. A model that needs
  to be swapped requires engine restart.
- **Engine refuses to boot if ModelBudget ≥ pod_limit.** Better a
  clear startup error than an engine that accepts zero sessions.
- **`aegis_engine_sessions_rejected_total{reason="session_budget"}`
  replaces the old `reason="budget"`** — callers that dashboarded
  the old label need to update.

### Implementation impact

Directly touches:

- `engine_cpp/src/session/resource_budget.{h,cc}` → rename to
  `session_budget.{h,cc}`; trim to what's now `SessionBudget`.
- `engine_cpp/src/session/model_budget.{h,cc}` → new.
- `engine_cpp/src/session/BUILD.bazel` → split targets.
- `engine_cpp/cmd/engine/main.cc` → startup order per API Shape
  above.
- `engine_cpp/src/session/session.cc` → `budget_->Reserve` /
  `Release` calls still use the renamed `SessionBudget*`; no
  behavioral change.
- Callers of the rejection-reason metric label dashboards.

**ADR-0010's original prose is preserved above — the pre-2026-04-15
`ResourceBudget` section still reads as it was written, with the
understanding that anywhere it says `ResourceBudget` the revised
code has `SessionBudget` (plus a companion `ModelBudget`).** Do
not delete the original prose; it is the historical record of the
Phase 1 decision that this revision extends rather than replaces.

### Phase 3b implementation checklist (tracked in ROADMAP 3b)

- [ ] Rename `ResourceBudget` → `SessionBudget` across the engine
- [ ] Add `ModelBudget` class + registration hooks in
      `WhisperEngine::Create` and `GGMLEmbedder::Create`
- [ ] Update `main.cc` startup order
- [ ] Update metric names + add the new labels
- [ ] Update `ARCHITECTURE.md` §6 capacity numbers to reflect the
      new ModelBudget baseline (separate commit — cross-cutting
      with other §6 revisions already accumulating)

## Related

- ADR-0004 Stateless Broadcast Relay (fail-fast session loss
  semantics)
- ADR-0005 Audio Ephemeral Policy (R1 lifetime; `SensitiveBytes` type)
- ADR-0006 Liveness and Disconnect Handling (Pause/Resume on
  transient host loss)
- ADR-0008 Monorepo Folder Structure (`engine_cpp/src/session/`,
  `engine_cpp/src/infra/`)
- ADR-0009 C++ Build and Toolchain (grpc-cpp and Abseil
  dependencies)
- ADR-0020 Engine owns inference — unified runtime for seed,
  query, ASR, future LLM (driver of the 2026-04-15 revision
  above)
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §10.6 Observability
- `ARCHITECTURE.md` §11 Known Limitations (L2 — C++ engine crash
  terminates session)
- [grpc-cpp sync server API](https://grpc.io/docs/languages/cpp/basics/)
- [Abseil Status guide](https://abseil.io/docs/cpp/guides/status)
