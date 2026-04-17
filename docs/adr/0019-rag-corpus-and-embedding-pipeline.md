# ADR-0019: RAG corpus + multilingual embedding pipeline

| Field    | Value                                                                   |
| -------- | ----------------------------------------------------------------------- |
| Status   | **Superseded in part by ADR-0020** (2026-04-15, same day): implementation mechanism moves from Python (`tools/rag/seed.py` + `FlagEmbedding`) to engine-owned (`engine --seed` + bge-m3 GGUF on the existing ggml runtime). The six numbered decisions below (corpus, chunking, model, vector store, distribution boundary, language-match contract) **remain in force**; only the seed-pipeline runtime changes. See the supersession note directly below. |
| Date     | 2026-04-15                                                              |
| Deciders | Project author                                                          |
| Context  | Phase 3 demo needs a concrete "who is this for?" story. The chosen persona — a foreign tourist asking questions about Taiwan, in their own language — forces decisions on corpus, chunking, embeddings, and the "local → cloud" distribution boundary. |
| Related  | ADR-0005 (audio ephemeral policy — different sensitivity class from RAG corpus), ADR-0008 (monorepo layout), ADR-0016 (Opus on engine — prior example of "code lives where the domain lives"), ADR-0018 (polyglot rationale), ADR-0020 (engine-owned inference — supersedes this ADR's implementation mechanism) |

> ### Supersession note — 2026-04-15 (same-day revision)
>
> This ADR's original plan put the seed pipeline in Python
> (`tools/rag/seed.py` using HuggingFace `FlagEmbedding` + PyTorch
> FP16). Later the same day, sharpening the runtime architecture
> for ADR-0020 (forthcoming) surfaced a structural bug in that
> plan: the C++ engine's query-time embedding would use **ggml
> bge-m3 at Q4_K_M quantization**, while Python-side seeding would
> use **PyTorch FP16**. Cosine similarity between the two
> encodings of the same text is typically ~0.97, not 1.0 —
> meaning the corpus vectors and the query vectors would live in
> **subtly different spaces**. Retrieval quality would silently
> degrade, and §Decision.5's "immutable corpus, reproducible
> index" guarantee would quietly fail to hold.
>
> The fix is unification, not patching. The engine already runs
> ggml (for whisper.cpp); adding bge-m3 GGUF Q4_K_M (~400 MB
> quantized) onto the same runtime is the same-stack extension.
> The engine gains a `--seed` subcommand that calls the same
> embedding code path the query handler uses, so **one embedder,
> one vector space, one model version** — for both seed time and
> query time, local and cloud.
>
> **In force, unchanged**:
>
> - §Decision.1 — corpus = zh-TW Wikipedia lead for Taiwan
> - §Decision.2 — Chinese-aware chunking rules (`。！？，` etc.)
> - §Decision.3 — `BAAI/bge-m3` as the embedding model
> - §Decision.4 — Qdrant as vector store
> - §Decision.5 — corpus in git, index as derivative artifact
> - §Decision.6 — language match at the LLM prompt layer
>
> **Revised by ADR-0020**:
>
> - **Runtime binding**: C++ engine, not Python sidecar. `engine
>   --seed` is the sole seed entry point; there is no `seed.py`
>   anywhere in the repo.
> - **Precision**: Q4_K_M via GGUF (throughout), not FP16 (seed)
>   + Q4 (query). One precision, one vector space.
> - **Toolchain impact**: Python is removed from the repo as a
>   runtime or seed-time surface. It remains only as a dev-tier
>   tool (pre-commit hooks), same category as `protoc`, `buf`,
>   `pnpm`.
> - **ADR-0018 thesis strengthened**: 2 runtime tiers (C++ engine,
>   Go gateway) now holds for BOTH hot path AND offline seed
>   path. Python never becomes a runtime tier.
>
> The numbered paragraphs below are retained as the decision
> record of the choices that remain in force, with the
> understanding that any code references to `tools/rag/seed.py`
> or `requirements.txt` describe a plan that did not ship. For
> the current seed mechanism, see ADR-0020 and the engine binary.

## Context

Aegis's user persona is a **chief-of-staff to a principal** — the person
who prepares a briefing the principal will read in five minutes. The
Phase 3 demo inhabits a softened, IP-safe version of that persona: an
**international tourist preparing a trip to Taiwan**, asking questions
about the island in whatever language the tourist speaks natively,
receiving answers in the same language, grounded in the
Traditional-Chinese Wikipedia page for Taiwan.

This is a concrete choice with several load-bearing technical
consequences that any reviewer should expect a pre-committed answer
for. Picking the wrong embedding model, the wrong chunking strategy,
or the wrong "what goes in git vs. what gets rebuilt" line produces a
demo that looks right in a screenshot but fails when the reviewer
actually types a query in Japanese or Thai.

The persona also dictates the **language-match contract**: query
language `L` in → answer language `L` out. That is not a retrieval
property; it is a generation-layer property controlled at the LLM
prompt. Conflating the two is the most common RAG mistake.

## Decision

Aegis ships a **reproducible-from-source RAG pipeline** with these
choices:

1. **Corpus**: the Traditional-Chinese Wikipedia article for Taiwan,
   checked into the repo at `docs/rag/taiwan.md` (plain Markdown,
   human-readable, Git-diffable, attributed under CC BY-SA 4.0 at
   the top of the file).
2. **Chunking**: Chinese-aware recursive character splitter with
   explicit separators (`\n\n`, `\n`, `。`, `！`, `？`, `，`, space).
   Target chunk size ≈ 450 characters; overlap ≈ 80. No word-based
   splitter — Chinese has no word boundaries and naive tokenization
   mangles sentences.
3. **Embeddings**: `BAAI/bge-m3` via `FlagEmbedding`. Open weights,
   runs locally on Apple Silicon via sentence-transformers-compatible
   tooling, multilingual across 100+ languages, 1024-dim dense
   vectors plus sparse weights. The dual dense+sparse capability is
   retained for a later hybrid-retrieval step; Phase 3 only uses the
   dense vectors.
4. **Vector store**: [Qdrant](https://qdrant.tech). Same API for
   local binary (in-process or sidecar container) and Qdrant Cloud.
   Rust-based, aligns with the Phase 4 Tauri direction; gRPC
   available, aligns with the existing contract discipline.
5. **Distribution boundary** — the central decision of this ADR:
   - `docs/rag/taiwan.md` — **in git**. Ground-truth corpus, diffable.
   - `tools/rag/seed.py` + pinned `requirements.txt` — **in git**.
     Anyone cloning the repo reproduces the index in ~30 seconds.
   - The built vector index (parquet, Qdrant snapshot, embedded
     `.bin` files) — **NOT in git**. See rationale below.
6. **Language-match contract**: enforced at the **LLM prompt layer**,
   not at retrieval. System prompt mandates "respond in the same
   language as the user's most recent query." Embedding retrieval
   is cross-lingual (bge-m3 brings EN/JA/KO/TH queries to zh chunks);
   the answer language is a generation concern.

### Explicitly deferred to follow-up ADRs

- **Runtime query-path binding**: whether the RAG query runs in the
  engine (C++ Qdrant client), the gateway (Go Qdrant client), or a
  Python retriever sidecar is not decided in this ADR. Seed pipeline
  is the stable part; query path is load-bearing on phase-4 product
  architecture. Flagged as Known Gap.
- **LLM choice and hosting**: the model that generates the final
  answer (Qwen 2.5 / Llama 3.3 / Gemma 2 / cloud) is ADR-0020's
  problem. This ADR only specifies that *whatever LLM runs* must
  be prompt-controlled for same-language output.

## Rationale

### Why bge-m3 over multilingual-E5-large

Both are strong multilingual embedding models. bge-m3 wins on three
axes that matter for this demo:

1. **zh quality**: BAAI is a Chinese research institute; bge-m3's
   zh-zh and zh-cross-lingual retrieval metrics edge out E5 on
   MIRACL-zh by ~2–3 points nDCG@10. For a demo centered on a
   zh-TW corpus, that is the most important number.
2. **Hybrid capacity retained for later**: bge-m3 emits dense +
   sparse + multi-vector representations from a single forward pass.
   Phase 3 uses dense only, but if Phase 4 adds keyword fallback
   (BM25-style) or ColBERT-style late-interaction reranking, the
   model is ready — no re-embedding the corpus.
3. **Freshness**: bge-m3 is a 2024 release; multilingual-E5 is 2022.
   A reviewer reading the commit log sees currency.

The cost is ~2.2 GB of model weights on first run. That is within the
`/models` convention (ADR-0009, CLAUDE.md Rule 6) and stays inside
the repo-local cache.

### Why Qdrant over pgvector / Weaviate / chromadb / FAISS

| Option    | Local | Cloud | zh filter | Sparse+dense | Ecosystem |
| --------- | ----- | ----- | --------- | ------------ | --------- |
| Qdrant    | ✅ single binary | ✅ managed | ✅ rich filters | ✅ named vectors | Rust-native, gRPC |
| pgvector  | ✅ via Postgres | ✅ managed (RDS/Neon) | ⚠️ SQL filters only | ⚠️ via extensions | Requires Postgres |
| Weaviate  | ⚠️ heavier binary | ✅ managed | ✅ | ✅ | Go-native |
| chromadb  | ✅ SQLite-backed | ⚠️ beta | ⚠️ limited | ❌ | Python-only, demo-oriented |
| FAISS     | ✅ in-process | ❌ | ❌ | ❌ | Library, not server |

Qdrant wins the tie-break on two axes: Rust aligns with the Phase 4
Tauri direction (shared language with the shell), and the same
client API covers local and cloud — "local first, cloud identical"
matches the sovereignty story the rest of the repo already argues.

### Why the corpus is in git but the index is not

This is the distribution boundary decision. Three alternatives were
on the table:

- **(A)** Commit corpus + commit pre-built vector index.
- **(B)** Commit corpus only; index rebuilt by `tools/rag/seed.py`.
  **← chosen.**
- **(C)** Neither corpus nor index in git; both built from external
  URLs at setup time.

**(A) is rejected** because binary vector files diff-noise every
regeneration and drift silently when the embedding model version
bumps. A reviewer `git log`ing the repo would see mystery churn on
files they cannot read. The vectors are also a **derivative artifact**,
and committing derivatives is the same anti-pattern as committing
`node_modules/` or compiled `.o` files.

**(C) is rejected** because it breaks the clone-and-run promise.
Wikipedia article content is versioned, can be edited, and is not a
stable URL. If the reviewer clones tomorrow and the article changed
overnight, the demo's retrieval quality diverges from whatever the
README claims.

**(B) is the principled middle**: the corpus is the immutable truth
(authored once, edited only deliberately, attributable), and the
index is a reproducible function of (corpus, seed.py, pinned model
version). The reviewer types one command and gets byte-for-byte the
same index the author has. This is what "immutable corpus,
reproducible index" means.

### Why the language-match contract lives at the prompt, not retrieval

This is the subtle one. A naive RAG implementation tries to filter
retrieval by query language, or embeds the question *and* a language
tag jointly. Both are wrong for our case:

- Our **retrieval target is the zh corpus** regardless of query
  language. A Japanese query *should* retrieve Chinese chunks; the
  cross-lingual embedding is the whole reason we picked bge-m3.
- The **answer language is a surface concern**, and surface concerns
  live at the generation layer. The LLM gets the zh chunks as
  context and a system instruction to reply in the query's language.

Implementation detail: we detect the query language via `langdetect`
(or equivalent) once per query, include it verbatim in the system
prompt slot, and let the LLM do the translation. This is cheaper and
more flexible than any embedding-side gymnastic.

## Consequences

### Positive

- **Demo has a single concrete persona** a reviewer can inhabit in
  seconds: "I'm planning a trip to Taiwan, I speak Japanese." The
  language-match behavior is the immediate wow-factor.
- **Reproducibility is enforced structurally**, not documented as
  aspiration. The reviewer verifies the story by running one command.
- **Sovereignty story stays coherent**: open-source embedding model
  (bge-m3, Apache 2.0), open-source vector DB (Qdrant, Apache 2.0),
  runs entirely on-device for local demo. No closed-API dependency
  on the hot path, consistent with ADR-0005's privacy posture.
- **Upgrade path to production is clear**: same `seed.py`, same
  Qdrant API, swap local endpoint for Qdrant Cloud URL. No
  re-embedding, no re-chunking.

### Negative / costs

- **First-run weight download**: ~2.2 GB for bge-m3. Mitigated by
  the existing `/models` convention and clear README messaging.
- **Python toolchain introduced**: `tools/rag/` is the first real
  Python surface in the repo. Per CLAUDE.md Rule 6, managed via
  `.venv` inside the repo. Adds one more toolchain to the preflight
  mental model; offset by keeping the surface tiny (one script).
- **Second-order: dense-only retrieval leaves quality on the table.**
  bge-m3's sparse + multi-vector capabilities go unused in Phase 3.
  Documented; opens space for a Phase 4 hybrid-retrieval ADR.
- **Qdrant is a new operational dependency**. For the *local* demo
  this is just a binary; for cloud deploy it adds a managed-service
  dependency. Cost bounded by free-tier limits at current scale.

## Alternatives considered

### A. Roll our own vector store

Pure-Python `numpy` cosine similarity over a DataFrame. Works for
~1000 chunks. Rejected: sets a bad precedent ("we can just
rebuild standard infra"), and the Qdrant binary is one `brew install`
or one binary download — the operational cost is smaller than the
"we invented our own thing" narrative cost.

### B. Closed-API embeddings (OpenAI, Cohere, Voyage)

Higher retrieval quality out-of-the-box; faster to set up. Rejected
on sovereignty grounds: putting a tourist's query in flight to a
third-party API undermines the on-device thesis. The demo's whole
point is "nothing leaves the machine unless the user opts in."

### C. Embed English translations instead of zh

Translate the corpus to English, embed the English, retrieve against
English queries. Rejected: loses zh semantic fidelity (names, place
names, historic references translate poorly), and adds a translation
step as a silent quality-loss surface. bge-m3 exists precisely to
make this workaround unnecessary.

## Implementation checklist

**Skeleton landed today (2026-04-15):**

- [x] `tools/rag/seed.py` — script skeleton with chunking + embedding
      + Qdrant upsert logic.
- [x] `tools/rag/requirements.txt` — pinned dep versions.
- [x] `.gitignore` already excludes `.venv/` — verified.
- [x] This ADR.

**Corpus + local demo (follow-up):**

- [ ] `docs/rag/taiwan.md` — fetch zh-TW Wikipedia article, clean
      up wiki markup → markdown, add CC BY-SA 4.0 attribution header.
- [ ] First end-to-end seed run: `python tools/rag/seed.py` produces
      a local Qdrant collection and logs chunk/embedding counts.
- [ ] Sanity query: EN ("what is Taiwan famous for?") retrieves
      zh chunks with cosine > 0.5.

**Query-path integration (deferred to ADR-0020):**

- [ ] Decide runtime binding (engine / gateway / sidecar).
- [ ] Connect LLM generation with language-match system prompt.
- [ ] `docs/incidents.md` entry if any non-trivial blocker surfaces
      during that integration.

**Cloud deploy (Phase 4):**

- [ ] Qdrant Cloud collection provisioned via IaC.
- [ ] CI step runs `seed.py --target=cloud` on deploy, upserts to
      the remote collection.
- [ ] Index version pinned by (corpus sha256, model name + version,
      chunker params) so "what index is running" is a deterministic
      query.
