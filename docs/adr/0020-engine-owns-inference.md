# ADR-0020: Engine owns inference — unified runtime for seed, query, ASR, future LLM

| Field    | Value                                                                                                                                                                                                                                                                                |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Status   | Accepted                                                                                                                                                                                                                                                                             |
| Date     | 2026-04-15                                                                                                                                                                                                                                                                           |
| Deciders | Project author                                                                                                                                                                                                                                                                       |
| Context  | ADR-0019 landed a Python seed pipeline that morning; same-day design review surfaced a seed/query embedding drift bug + a broader question of whether Python becomes a runtime tier. This ADR locks the engine-owned, unified-inference answer and makes the consequences explicit. |
| Related  | ADR-0005 (audio ephemeral policy — sovereignty posture), ADR-0009 (C++ build + whisper.cpp), ADR-0010 (engine runtime architecture — ResourceBudget), ADR-0016 (Opus on engine — prior "code lives where the domain lives" example), ADR-0017 (N:N gateway-engine topology), ADR-0018 (polyglot rationale, Recommendation A vs B), ADR-0019 (RAG corpus + embedding pipeline — this ADR supersedes its implementation mechanism) |

## Context

Three forces converged in a single afternoon's design review and
forced this ADR to exist:

1. **ADR-0019's drift bug**. Seeding with PyTorch + FlagEmbedding
   (FP16) and querying with ggml bge-m3 (Q4_K_M quantization)
   would put the corpus vectors and the query vectors in **two
   subtly different vector spaces** (cosine similarity ~0.97
   between the two encodings of the same text, not 1.0).
   Retrieval quality silently degrades. §Decision.5 of ADR-0019
   ("immutable corpus, reproducible index") quietly fails.

2. **A latent polyglot-tier creep**. `tools/rag/seed.py` was
   framed as a "dev tool," but its position inside the RAG
   pipeline — batch-producing the very vectors the runtime
   queries against — put it closer to the (B) runtime-service
   tier than the (A) dev-tool tier. ADR-0018's thesis
   ("polyglot is C++/Go/TS; Python stays in the tool tier") was
   a page away from breaking the first time a reviewer looked
   hard at where the embeddings actually come from.

3. **The "SageMaker question"**. A reasonable reading of the
   industry asks: "Most production shops upload a doc, call a
   managed inference service (SageMaker / Bedrock / OpenAI /
   Cohere), and push the result into a vector DB. Why isn't
   Aegis doing that?" The honest answer is that **most
   production shops pick managed inference for cost + ops
   economics**, not for architectural superiority. Aegis's
   positioning (privacy-as-structural-property, chief-of-staff
   sovereignty, nothing-leaves-the-machine) is incompatible
   with a SaaS inference dependency on the hot path.

All three pressures point at the same answer — and this ADR is
the place to write it down so the decision doesn't keep
re-surfacing.

## Decision

**The engine owns all model inference.** One binary, one ggml
runtime, one model-weight registry, one embedder — for seed
time, query time, ASR, and any future LLM. Python and
third-party managed inference both stay off the runtime.

Seven numbered sub-decisions:

### 1. Engine hosts every model the product uses

`engine_cpp/` is the inference tier. whisper.cpp is there
today; bge-m3 (GGUF Q4_K_M) joins it for embedding; a future
LLM (Qwen 2.5 / Llama 3.3 / Gemma — ADR-to-be) will join the
same process. One address space, one Metal/SIMD context, one
memory budget.

No separate Python service, no gRPC hop to a managed endpoint,
no sidecar container whose version drifts from the engine's.

### 2. Python is a dev/CI tool tier only

Python lives in the repository as part of `pre-commit` and the
occasional one-off script. It **never runs in a production
container** and **never touches any vector the runtime
queries against**. This is the same category `protoc`, `buf`,
and `pnpm` occupy: essential to the workflow, invisible to the
runtime.

Concretely: no `tools/rag/seed.py` (deleted in `0c7162b`); no
`requirements.txt` under `tools/rag/`; no Python Dependabot
stanza. If `pre-commit` ever needs a new Python dependency,
that's a tool-tier addition and is fine.

### 3. Runtime tiering stays at two languages: C++ + Go

ADR-0018 Recommendation A states the polyglot thesis as "C++
engine + Go gateway + TS frontend + future Rust Tauri." This
ADR hardens that: **the C++ side of the line holds for both
hot path AND offline seed path**. Python cannot drift in as a
third runtime tier through the RAG pipeline back door.

### 4. Model format: GGUF for everything new

Every new model the engine loads is **GGUF-formatted and
loaded via the existing ggml runtime**:

- whisper.cpp already uses ggml (ADR-0009).
- bge-m3 has community GGUF ports (e.g. `CompendiumLabs/bge-m3-gguf`).
- Llama 3 / Qwen 2.5 / Gemma all ship GGUF via llama.cpp's
  tooling.

One model loader, one quantization regime, one file format to
audit. This is also what `ollama` and `llama.cpp server`
standardized on, and following their convention means we can
re-use their model conversion scripts and `/models/` layout.

### 5. Quantization defaults

- **Embedding models (bge-m3 et al.)**: **Q4_K_M**. ~400 MB
  for bge-m3 vs 2.2 GB full precision. Retrieval quality loss
  vs FP16 is ~2–3% cosine-similarity drift on bench suites,
  which is well below the noise floor of chunking / system
  prompt variation at the demo scale.
- **Speech models (whisper-tiny/base/small)**: stay at the
  default shipped by whisper.cpp (FP16 ggml). ASR quality is
  load-bearing for the product's core promise — "we transcribe
  the meeting accurately" — so quantization gains aren't worth
  WER regression risk.
- **LLM (when it lands)**: decision deferred, but default
  expectation is Q4_K_M (llama.cpp community consensus for
  local deploys).

### 6. Cloud mode runs the same engine binary, not SageMaker

Cloud-mode deployments run the **same engine container** that
local mode uses. The cloud's seed workflow is:

```
[User in browser]
    ↓  POST /api/v1/corpus (multipart, .md file)
[Gateway (Go)]
    ├─ validate, store corpus in object storage
    ├─ enqueue seed job
    └─ return { corpus_id, status: "pending" }

[Engine replica (C++) picks up the seed job]
    ├─ download corpus from object storage
    ├─ chunk + embed via the same code path the query handler uses
    ├─ upsert into Qdrant Cloud collection
    └─ update corpus status: "ready"

[User polls / subscribes via WS]
    └─ status=ready → corpus is queryable in the next meeting
```

One engine, two deploy contexts. The seed-time embedder and the
query-time embedder are **guaranteed to produce identical
vectors** because they are literally the same function call in
the same binary.

### 7. Hybrid escape hatch is documented but not default

A product team whose economics favor managed inference (ADR-0018
Recommendation B territory) **may** configure the cloud
deployment to delegate embedding to SageMaker / Bedrock / a
hosted bge-m3 endpoint. The interface lives at the **storage +
retrieval** seam:

```cpp
// engine_cpp/src/inference/embedder.h
class Embedder {
 public:
   virtual absl::StatusOr<std::vector<float>>
   Embed(std::string_view text) = 0;
};
```

Default implementation: `GGMLEmbedder` (loads GGUF, embeds
locally). Optional implementation: `RemoteEmbedder` (gRPC /
HTTPS to a SageMaker endpoint). **Seed and query use the same
`Embedder*` instance** — so whichever implementation is chosen,
vectors in a given cluster are internally consistent.

**Default remains `GGMLEmbedder`.** `RemoteEmbedder` is an
escape hatch for a future deployment that has already
surrendered the local-first thesis for product-scale economic
reasons; it is not the architectural recommendation for this
repository.

## Rationale

### Why engine-owned over Python sidecar

Quick-reference table:

| Dimension                 | Engine-owned (C++ GGML)         | Python sidecar (FlagEmbedding) |
| ------------------------- | ------------------------------- | ------------------------------ |
| Container image (w/model) | ~500 MB (Q4_K_M)                | ~3–5 GB (PyTorch + weights)    |
| Runtime tiers             | 2 (C++/Go)                      | 3 (+ Python)                   |
| IPC on hot path           | zero                            | ~5–10 ms + serialization       |
| Vector space consistency  | guaranteed (same binary)        | drift risk vs query path       |
| Dev iteration speed       | slower (C++ recompile)          | faster (Python hot reload)     |
| Memory competition        | shared context (SIMD / Metal)   | separate processes             |
| Hiring / onboarding       | existing C++ skill pool         | new Python-runtime expectation |

PyTorch + Python ML stack's container cost is ~2 GB **before
any model weights load**. ggml's inference runtime is a few
hundred lines of C that disappears into the engine binary. The
"Python is easier" argument wins on dev velocity for
*corpus tuning*, but corpus tuning is offline batch work — not
the shape the runtime needs to optimize for.

### Why engine-owned over SageMaker / Bedrock

This is the rationale the "SageMaker question" deserves:

**What SageMaker buys you**:

- No GPU provisioning.
- No model weight management.
- Pay-per-inference at low scale.
- Faster time-to-market for an MVP.

**What it costs you** (load-bearing for Aegis specifically):

- **Sovereignty**. Every query + every seed goes to AWS. ADR-0005
  R3's "audio ephemeral" promise doesn't extend to a chief-of-
  staff's typed query that gets shipped to us-east-1.
- **Offline capability**. Chief-of-staff on a plane, in a tunnel,
  at a client site with enterprise firewall — SageMaker doesn't
  work without network.
- **Vendor lock-in**. SageMaker-specific SDK, SageMaker-specific
  auth, SageMaker-specific model catalog. Non-trivial to unwind.
- **Portfolio signal**. "We call SageMaker" does not demonstrate
  ML-infrastructure competence; it demonstrates ability to read
  AWS docs. The harder skills (quantization, ggml runtime,
  memory budgeting, model co-location) only surface on the
  embedded-inference path.

For a real product team with ARR growth pressure and no ML-ops
headcount, SageMaker is often the right economic choice (this
is ADR-0018 Recommendation B). For this portfolio repository
demonstrating deep-stack capability, it is deliberately not.

### Why unified seed + query embedder is non-negotiable

Even a *SageMaker* architecture would hit this: if seeding
calls `sagemaker-endpoint-A` and querying calls
`sagemaker-endpoint-B`, and A + B are pinned to different
model versions / quantizations, **vectors in A's space and
B's space are not meaningfully comparable**. You get retrieval
that looks sort-of right but silently drops precision.

So "one embedder for seed + query" is an *independent* design
principle that applies regardless of engine-owned vs. managed.
This ADR happens to enforce it trivially — one binary, one
function — but any design that doesn't enforce it is fragile.

### Why GGUF / ggml specifically

ggml is already in this repo via whisper.cpp (ADR-0009). Adding
bge-m3 via ggml is:

- **Zero new runtime**: ggml is what's already linked.
- **Aligned with llama.cpp ecosystem**: if we add an LLM later,
  it's the same loader, same quantization story, same tooling
  for model conversion (`llama-cpp/convert_hf_to_gguf.py`).
- **Hardware-agnostic**: Metal / CUDA / CPU fallback all work
  through the same interface.

Alternative inference runtimes considered:

- **ONNX Runtime**: mature, cross-platform, but introduces a
  second inference framework alongside ggml. Rejected for
  complexity.
- **TensorRT**: NVIDIA-only, so rejected for the local-first
  cross-platform thesis.
- **Candle (Rust)**: attractive in isolation but would introduce
  Rust as a fourth runtime language, which doesn't pay its own
  way here.

## Consequences

### Positive

- **Vector space consistency is structural, not disciplinary.**
  The seed-time embedder is the query-time embedder is the
  same function call. No drift class of bugs.
- **Python is permanently off-runtime.** The creep that
  almost happened (seed.py → requirements.txt → Dependabot
  stanza) is now impossible without an ADR revision.
- **ADR-0018's polyglot claim holds on both hot and offline
  paths.** This is the portfolio claim that stops getting
  re-litigated.
- **Cloud and local deployments share one binary.** No two
  codebases to keep in sync, no conditional logic paths for
  "are we local or cloud."
- **Future LLM lands in the same process** alongside whisper
  and bge-m3. ASR → embed query → retrieve context → generate
  answer is one Metal/SIMD context, zero IPC, bounded memory.
- **Container image stays well under what an equivalent
  Python-stack solution would ship.** ~500 MB for engine +
  bge-m3 Q4_K_M, vs ~3–5 GB for a PyTorch-based sidecar.

### Negative / costs

- **Engine memory footprint grows ~400 MB** (bge-m3 Q4_K_M).
  ADR-0010's ResourceBudget default (200 MB per-session
  reservation) needs revision to separate "per-session" from
  "shared model weights" budgets. The existing
  `kDefaultReservationBytes` constant is per-session; the
  new global model-weights budget is ~500 MB (whisper tiny.en
  ~75 MB + bge-m3 Q4_K_M ~400 MB + overhead).
- **bge-m3 GGUF support maturity is less proven than
  FlagEmbedding**. The community-maintained GGUF conversions
  work but are not upstream BAAI. A first-run validation
  (cosine-similarity check vs FlagEmbedding reference) is on
  the implementation checklist below.
- **Dev iteration on chunking / prompt engineering is slower**
  in C++ than in Python. For corpus *tuning* workflows, an
  operator may prefer a Python notebook; this is fine as long
  as the notebook is clearly marked "experimentation, not
  production." The notebook's embeddings must not feed the
  production Qdrant collection (enforced by operational
  discipline — no ADR-level mechanism).
- **First-run GGUF download** is ~400 MB and must land in
  `/models/` per CLAUDE.md Rule 6. Add to the existing
  `tools/scripts/download_models.sh` script.
- **Engine build time grows marginally** — ggml's bge-m3 path
  adds a few source files but reuses the existing ggml
  compilation unit. Cold engine build previously ~90 s on
  darwin-arm64; estimate ~100–110 s after integration.

## Alternatives considered

### A. Python sidecar for embedding (runtime tier)

Covered in Rationale. Rejected: 3-tier runtime, container
bloat, drift risk, ADR-0018 thesis break.

### B. SageMaker / Bedrock / OpenAI / Cohere managed inference

Covered in Rationale. Rejected for this repo; escape hatch
documented in Decision §7 for a future product deployment
that wants it.

### C. Gateway (Go) via cgo to libopus-style bge-m3

The same pattern ADR-0016 rejected for Opus. bge-m3 is a
transformer; its cgo surface is not trivial, and the
gateway's domain is BFF, not inference. Rejected on
domain-boundary grounds (identical to ADR-0016 §"Why not A'").

### D. Qdrant's built-in fastembed / server-side embedding

Qdrant Cloud offers `fastembed` integration: push raw text,
Qdrant computes the embedding with its chosen model and
stores the vector. Attractive for its simplicity.

Rejected because:

- Model catalog is Qdrant-defined; bge-m3 is not currently a
  first-class offering.
- Locks embedding choice to the vector DB vendor; switching
  vector DBs later means re-embedding.
- Violates the "one embedder, one vector space" principle
  if local mode embeds in-engine and cloud mode embeds in
  Qdrant.

### E. Hybrid — local uses engine, cloud uses SageMaker

Initially attractive: local-first where sovereignty matters,
managed-at-scale where economics matter. Rejected as a
*default* because it forces two codebases to co-exist with a
drift-prevention discipline that's easy to break. Kept as a
*deployment-time option* via the `Embedder` interface in
Decision §7 — the architectural answer is "the engine owns
embedding, end of story"; the deployment answer is
"you can swap the `Embedder` impl if your org's economics
demand it."

## Implementation checklist

**Engine-side inference integration:**

- [ ] `engine_cpp/src/inference/embedder.h` — abstract
      `Embedder` interface (Embed(text) → vector).
- [ ] `engine_cpp/src/inference/ggml_embedder.{h,cc}` —
      default implementation wrapping ggml's bge-m3 GGUF
      loader.
- [ ] `engine_cpp/third_party/bge_m3/` — third-party wrapper
      downloading the Q4_K_M GGUF from the pinned upstream
      (sha256 + mirror per ADR-0009 pattern).
- [ ] Validation test: load same corpus with both
      FlagEmbedding (reference, in a test-only Python
      scratch) and `GGMLEmbedder`, assert cosine-similarity
      ≥ 0.95 on 100 random chunks. Scratch Python does NOT
      land in the repo — run locally to gate the
      integration, then delete.

**Seed CLI:**

- [ ] `engine --seed --corpus PATH --target={local|cloud}`
      subcommand in `engine_cpp/cmd/engine/`.
- [ ] C++ markdown chunker with the same separator list
      ADR-0019 §Decision.2 defined
      (`\n\n`, `\n`, `。`, `！`, `？`, `，`, space).
- [ ] Qdrant C++ client via direct gRPC stubs generated from
      [Qdrant's proto](https://github.com/qdrant/qdrant/tree/master/lib/api/src/grpc/proto).

**ResourceBudget revision (ADR-0010 follow-up):**

- [ ] Split `ResourceBudget` into `ModelBudget` (process-
      global, ~500 MB for whisper + bge-m3) and
      `SessionBudget` (per-session, existing
      `kDefaultReservationBytes`).
- [ ] Update ADR-0010 with the new split.

**Cloud seed orchestration (Phase 4):**

- [ ] Gateway endpoint `POST /api/v1/corpus` accepting
      `multipart/form-data` (.md file + metadata).
- [ ] Object-storage write (S3 / EFS).
- [ ] Seed job queue (SQS / Step Functions / k8s Job).
- [ ] Engine replica picks up seed jobs via a dedicated RPC
      (`SeedCorpus` streaming or batch endpoint).
- [ ] Status-polling endpoint or WS push for the browser.

**Local UX (Phase 3 / 4 — Tauri):**

- [ ] File picker in the Tauri shell calling local engine's
      `SeedCorpus` over the existing gRPC channel.
- [ ] Same UX as cloud upload form — single "file browse →
      select → seed" flow regardless of deployment target.

**Docs sync:**

- [ ] ADR-0019 supersession callout already landed
      (commit `0c7162b`).
- [ ] ADR-0010 revision (above).
- [ ] `README.md` ADR index — row for 0020.
- [ ] Update `docs/rag/taiwan.md` header once `engine --seed`
      is real (today it forward-references this ADR).
