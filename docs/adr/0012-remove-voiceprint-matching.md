# ADR-0012: Remove Voiceprint Matching — Question-Driven Hints Only

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: Voiceprint-related sections of ADR-0005 (to be rewritten) and ARCHITECTURE.md §9 (to be updated)
- **Superseded by**: —

## Context

Earlier design work (ARCHITECTURE.md §4 step 6, the original ADR-0005
"Audio and Voiceprint Ephemeral Policy", and ARCHITECTURE.md §9.3 /
§9.8) assumed that Aegis would capture per-speaker **voiceprint
embeddings** at the start of every meeting. The embedding was meant
to answer one product question:

> "Which anonymous diarization label (`Speaker_0`, `Speaker_1`, …)
> corresponds to **The Boss**, so the AI can personalize its hint
> generation to help the boss respond?"

The flow was: host presses "New Meeting" → the boss says a short
enrollment phrase ("test123") → whisper extracts an embedding → that
embedding lives in C++ engine RAM for the session → diarization
output is matched back to "The Boss" via cosine similarity → hints
are framed specifically for the boss's next utterance.

This design had real privacy cost:

- Voiceprints are **biometric data** under GDPR Art. 4(14) / Art. 9,
  BIPA (Illinois 740 ILCS 14, with private right of action and
  nine-figure settlement precedents), Texas CUBI, and CCPA's
  "sensitive personal information" category.
- Even RAM-only handling required an explicit consent capture flow
  and a persistent consent ledger (ARCHITECTURE.md §9.3) as the
  evidentiary artifact for compliance audits.
- The C++ engine required an additional embedder model (~0.5 GB
  RAM), a cosine matcher module, and a session-scoped voiceprint
  vault.
- The UX required an enrollment step ("please have the boss say
  'test123'") at the start of every meeting.
- The ARCH §9.8 section alone was 30+ lines of compliance-posture
  prose devoted to a single feature.

This ADR captures the decision to **remove voiceprint matching
entirely** and replace the personalization mechanism with a simpler,
more powerful question-driven hint model. The product also gets a
**positive re-definition** from "defensive prompter" to "real-time
truth assistant" as a side effect.

## Decision Drivers

- **D1. Biometric data is the highest-risk category we handle.**
  Removing it eliminates an entire tier of regulatory burden (GDPR
  Art. 9, BIPA, Texas CUBI, CCPA sensitive) rather than merely
  mitigating it.
- **D2. UX friction from enrollment is avoidable.** The "say test123"
  step is a tax on every meeting start.
- **D3. Engine memory budget is load-bearing for the 16 GB local
  ceiling** (ARCHITECTURE.md §6). Dropping the embedder (~0.5 GB)
  directly raises the number of concurrent sessions per engine pod.
- **D4. Code surface reduction.** A voiceprint module, cosine
  matcher, consent ledger write-through, and per-session vault are
  all non-trivial code that would need tests, threat modeling, and
  lifetime-correctness reasoning.
- **D5. Product positioning improvement** (surfaced during design
  review). A question-driven model generalizes better than a
  boss-centric model and actually strengthens the value proposition
  in adversarial meeting contexts (see "Product Definition Shift"
  below).
- **D6. Regret minimization.** Adding voiceprint matching later if
  we find we need it is a purely additive change (new ADR, new
  module, new ADR-0005 revision). Removing it later — after
  shipping, after collecting voiceprint data from early customers,
  after building the legal / consent apparatus — is dramatically
  more expensive. **The expensive direction is voiceprint-in**;
  voiceprint-out is cheap.

## Considered Options

### Option A — Keep voiceprint matching as originally designed

Continue with the enrollment-based personalization: users say
"test123" at meeting start, engine extracts embedding, diarization
labels map to "The Boss", hints are framed specifically for the
boss. Full ADR-0005 compliance apparatus remains.

### Option B — Remove voiceprint matching; keep diarization; question-driven hints ✅ CHOSEN

Drop voiceprint enrollment and cosine matching entirely. Keep
speaker diarization (anonymous labels `Speaker_0`, `Speaker_1`, …)
because the labels themselves are not biometric and materially
improve transcript readability. Shift the hint trigger from
"speaker identity" to "question detected in the transcript,
regardless of speaker" — any participant asking a question triggers
a RAG-backed hint.

### Option C — Remove voiceprint AND diarization

Drop both biometric matching and speaker attribution. Transcript
becomes a single unattributed stream of text.

## Decision Outcome

**We choose Option B: remove voiceprint matching, keep diarization,
adopt question-driven hints.**

### Why Option B

#### Privacy wins (D1)

| Concern | Before (Option A) | After (Option B) |
|---|---|---|
| GDPR Art. 9 special category | Applies; explicit consent required | **Not applicable** — we process no biometric data |
| BIPA private right of action | Applies; written consent + retention policy required | **Not applicable** |
| Texas CUBI / CCPA sensitive PI | Applies | **Not applicable** |
| Breach notification for biometric | Must have a plan | **Structurally impossible** — nothing to breach |
| Consent ledger | Required, 7-year retention | Simplified to general audio-processing consent |
| Legal review cost per customer geography | High (regulated industries require sign-off) | Negligible |

The transition from "mitigated biometric handling" to "zero
biometric handling" is a **categorical** improvement, not a
gradual one. Regulated-industry sales (finance, healthcare, legal)
become materially easier because biometric data's handling is
often the first question enterprise legal teams ask.

#### Resource wins (D3)

| Budget line | Option A | Option B | Delta |
|---|---|---|---|
| Fixed model overhead | ~3.0 GB | ~2.5 GB | **−0.5 GB** (drop embedder) |
| Per-session budget | ~200 MB | ~150 MB | **−50 MB** |
| Phase 1 sessions per 8 GB engine pod | ~25 | ~36 | **+44%** |

Concrete: same hardware, 44% more concurrent sessions. This is not
a trivial win for unit economics.

#### UX wins (D2)

| Step | Option A | Option B |
|---|---|---|
| Mic permission grant | ✅ | ✅ |
| "Say test123" enrollment | ✅ (~10s friction) | ❌ (removed) |
| Speaker tagging UI | ✅ (host clicks "this is The Boss") | ❌ (removed) |
| Explicit consent checkbox | ✅ (biometric disclosure) | ❌ (folded into general audio processing notice) |
| Time to first hint | ~30 s after meeting start | ~5 s after meeting start |

#### Code surface wins (D4)

Eliminated modules / concerns:

- `engine_cpp/src/voiceprint/` — entire directory
- Session-scoped voiceprint vault (allocation, lifetime, tests)
- Cosine similarity matcher
- Embedder model loader, SHA256 verification, `manifest.json` entry
- Enrollment UX state machine in the frontend
- Consent ledger biometric-specific write path + audit trail
- ARCH §9.8 Voiceprint as Special-Category Biometric Data section
- A large fraction of ADR-0005 (the voiceprint sub-policy)
- 3–5 STRIDE threats in `docs/threat-model.md` (all under
  Information Disclosure I1..I14 related to voiceprint)

#### Product definition shift (D5) — the adversarial insight

This is the **non-obvious** win that emerged during design review.

The original mental model (Option A):

> AI knows who "The Boss" is. When someone asks the boss a question,
> AI prepares an answer for the boss.

The new mental model (Option B):

> AI listens to everyone. When **any** question is asked, AI
> retrieves the factual answer from the RAG corpus and surfaces it.
> The host sees this alongside the conversation.

At first glance these look equivalent for the common "client asks,
boss answers" case — and they are. But in adversarial contexts
(negotiations, media interviews, depositions, press conferences,
regulatory testimony), the new model **unlocks a second mode** that
did not exist in Option A:

**Fact-checking the counterparty in real time.**

| Context | Option A behavior | Option B behavior |
|---|---|---|
| Negotiation: counterparty claims "last time we paid you $X" | AI helps boss respond politely | AI pulls the actual transaction from the RAG corpus; boss sees the discrepancy and can push back with authority: "My records show $Y" |
| Press conference: reporter asks "did you approve this?" | AI helps boss give compliance-safe answer | AI pulls the actual approval record; boss answers consistently with the record |
| Deposition: opposing counsel asks a leading factual question | AI helps boss answer carefully | AI pulls the relevant calendar / email evidence; boss stays factually consistent with the record |
| Media interview: journalist asks "will you commit to Y?" | AI helps boss find safe deflection | AI pulls prior public statements; boss stays on-message with past positions |

In every case, the boss gets **both a defensive position and an
offensive position**. The defensive position ("what do I say if
asked this?") was present in Option A. The offensive position
("the other party is wrong, and here is the evidence") was not.

The informal product owner phrasing for this, captured verbatim in
the design review so it does not get diluted:

> 「如果是老闆反問提問者答錯就可以嘴他。」
>
> ("If the boss is the one asking and the counterparty gets it
> wrong, the boss can call them out.")

This upgrades Aegis's positioning from **"defensive prompter"** to
**"real-time truth assistant"**. The existing tagline in `README.md`
— *"Turn every remote meeting into a strategic advantage"* — was
ambiguous under Option A but becomes **exactly right** under
Option B. No rebranding is required.

### Why Not Option A

- **Violates D1 severely.** The biometric data burden is the single
  largest compliance cost in Aegis's original design.
- **Violates D3.** Wastes ~0.5 GB on the embedder, reducing
  concurrent session capacity by ~30%.
- **Violates D5.** Locks us into a narrower mental model that
  undersells the product's adversarial value.
- **Violates D6.** Voiceprint-in is much harder to back out of later
  (would need to notify customers who gave biometric consent,
  handle deletion requests, possibly refund regulated-industry
  contracts).
- Addresses a use case ("personalized hints for the boss") that is
  more than adequately covered by question-driven hints in the
  common case, and is strictly worse in adversarial cases.

### Why Not Option C (drop diarization too)

- **Regresses transcript readability.** A transcript without speaker
  attribution is a wall of text. Users (especially the host
  reviewing a meeting post hoc) lose the ability to see who said
  what.
- **Saves nothing on compliance**, because anonymous diarization
  labels are **not** biometric data. GDPR Art. 25 "Data Protection
  by Design and by Default" explicitly blesses pseudonymous speaker
  labels as a privacy-preserving technique (ARCHITECTURE.md §9.2).
- **Saves modest resource cost** (~1 GB diarization model), but
  the UX regression is not worth it. If future load tests show
  diarization is the bottleneck, revisit then.

Option C is correctly rejected; diarization stays.

## What Stays the Same

- **Audio PCM is still ephemeral**, RAM-only, session-scoped, per
  the rewritten ADR-0005 "Audio Ephemeral Policy".
- **R1 through R7 enforcement requirements** (core dump disabled,
  swap disabled, log formatter whitelist, OTLP attribute allowlist,
  tmpfs-only temp, no PVC on audio namespace, debug dump compiled
  out) remain mandatory. They still apply to audio PCM even though
  voiceprint is removed.
- **Transcript statelessness (ADR-0004)** is unchanged — the Go
  Gateway still holds no content.
- **Liveness and disconnect handling (ADR-0006)** is unchanged.
- **Stream control events** (PAUSE / RESUME / END_STREAM on the
  C++ Engine ingest stream) are unchanged.
- **Speaker diarization** continues, producing anonymous labels
  `Speaker_0`, `Speaker_1`, ….
- **RAG corpus** still drives hint generation, just with a different
  trigger mechanism.

## How Question Detection Works (Phase 1)

**Not enforced by this ADR** — implementation detail — but noted so
the Phase 1 engineer does not rediscover it:

1. whisper.cpp large-v3 emits transcript text with punctuation,
   including `?` for detected interrogatives. Treat `?` as the
   primary signal.
2. Fall-back heuristics for languages / models that under-punctuate:
   - English: leading "wh-" words (`who`, `what`, `when`, `where`,
     `why`, `how`, `which`), yes/no starters (`is`, `are`, `do`,
     `does`, `can`, `could`, `would`, `should`).
   - Traditional / Simplified Chinese: sentence-final 「嗎」/「嗎？」,
     「呢」, 「吧」 (less reliable), question-word starters 「誰」
     「什麼」「為什麼」「怎麼」「哪裡」「何時」.
   - Code-switched utterances: apply both rule sets.
3. Phase 2+ upgrade path: replace heuristics with a small
   question-classification model or a `jina-embeddings-v2`-class
   classifier. Do not use a full LLM for this — latency matters.

The proto schema (`proto/aegis/v1/aegis.proto`) carries a
`bool is_question = 9;` field on `TranscriptSegment` so the engine
can mark segments as questions, the UI can highlight them, and the
hint generator can trigger on them without re-parsing.

## Future Outlook: Explicit Query (Formerly "AskRAG")

During the design discussion, a separate gRPC method `AskRAG` was
briefly defined on the Gateway service to allow the host to
**explicitly type** a question at any time. That method is **removed
from the MVP proto** because:

- The target user is in a live meeting and cannot reasonably type
  queries.
- Any query a human would reasonably type can instead be spoken
  aloud (or spoken by a staff member into a private microphone),
  captured by whisper, detected as a question, and answered
  automatically via the same RAG+LLM pipeline.
- The two paths produce identical output via identical machinery;
  only the trigger differs. Automatic trigger matches the UX;
  explicit trigger does not.

**When it might come back** (Phase 5+ or later):

- Non-real-time scenarios: pre-meeting prep ("given this brief,
  what are likely questions I'll face?"), post-meeting follow-up
  ("summarize what was said and flag action items").
- Offline / asynchronous use cases where the user IS at a desk and
  can type.

**Implementation constraint if AskRAG is re-added** (read this
before writing code):

Under the current ADR-0010 threading model ("1 session = 1
thread"), a naive implementation of AskRAG that injects the query
into the session's existing thread **would contend with whisper
inference for CPU/GPU cycles**. This would cause audible latency
spikes in transcript delivery for every explicit query — bad.

The correct implementation, when AskRAG is added, is to ensure
the RAG+LLM subsystem runs on a **separate thread / thread pool /
worker**, not on the session thread:

- **Option 1**: a dedicated `Engine.AskRAG` RPC on the Engine
  service that spawns a worker from a dedicated pool. Audio
  session thread is never blocked.
- **Option 2**: fold both automatic hint generation and explicit
  queries into a shared **RAG worker pool** that the session
  thread dispatches to. This is consistent with ADR-0010's
  "Phase 2+ upgrade path to model (iii) MPSC queue + worker
  pool" and is the cleanest long-term architecture.

Either option is acceptable; injecting AskRAG into the session
thread is **not acceptable**. Document this in ADR-0010 as well
when the upgrade happens.

## Consequences

### Positive

- GDPR Art. 9 / BIPA / CCPA biometric compliance burden **structurally
  eliminated**.
- Consent ledger simplifies to general audio-processing notice (no
  biometric-specific path).
- Engine memory budget frees up ~0.5 GB → **+44%** concurrent session
  capacity per pod (Phase 1 estimate: ~25 → ~36 sessions on 8 GB).
- Per-session budget drops ~50 MB.
- Zero enrollment UX friction; time to first hint drops from ~30 s to
  ~5 s.
- `engine_cpp/src/voiceprint/` directory eliminated; ~1–2 modules and
  their tests never need to be written.
- ~3–5 STRIDE threats in `docs/threat-model.md` disappear.
- ARCH §9.8 section deleted; §9.1 / §9.3 / §9.4 / §9.5 simplified.
- Product positioning upgraded from "defensive prompter" to "real-time
  truth assistant" without any code or tagline change.
- Adversarial meeting use cases (negotiation, deposition, press) gain
  genuinely new capability.

### Negative

- **Hint personalization is gone** — hints are generated for "the
  meeting" rather than "the boss specifically". In practice this is
  the same hint content because questions are the trigger and the
  answer does not depend on who's asking. Still, this is a loss of
  theoretical expressive power that some future feature might want
  back.
- **No "speaker X is the boss" ground truth** — future features like
  "send the boss a private side-channel hint the client cannot see"
  would require a different mechanism (e.g., a pre-shared secret per
  participant seat) rather than voiceprint identity.
- **The ADR set and ARCH set need a refactor pass** — we are
  intentionally taking this cost now, at design time, so it does not
  compound into a code refactor later.

### Risks

- **Adversarial framing requires trustworthy RAG corpora.** If the
  RAG corpus contains stale, wrong, or partial data, the "real-time
  truth assistant" gives the boss confidence in a false answer. The
  product is now more valuable AND more dangerous when the corpus
  is wrong. Mitigation: corpus versioning and per-corpus confidence
  indicators are Phase 2+ concerns. For MVP, document this clearly
  in `CONTRIBUTING.md` and user-facing docs: "The corpus is the
  source of truth. Curate it carefully."
- **Question detection quality determines hint quality.** If
  question detection under-fires (misses questions), hints are
  sparse. If it over-fires (flags non-questions as questions),
  hints are noisy. Phase 2 tuning via the WER golden audio fixtures
  (ADR-0011) can help. Neither failure mode is catastrophic.
- **Re-adding voiceprint later is harder than never having removed
  it** — but this is intentional and asymmetric in our favor.
  Removing first and revisiting with real demand data is the
  correct direction per D6.

## Files Affected by This Decision

This ADR triggers an architecture refactor covering:

- `docs/adr/0005-audio-voiceprint-ephemeral-policy.md` — rename to
  `0005-audio-ephemeral-policy.md`; remove voiceprint sections;
  keep R1–R7 enforcement intact.
- `ARCHITECTURE.md` §4 Data Flow — remove voice enrollment step.
- `ARCHITECTURE.md` §6 — recalculate memory budget.
- `ARCHITECTURE.md` §9.1 Layered Privacy Model — remove Layer 2.
- `ARCHITECTURE.md` §9.3 Consent Capture — simplify.
- `ARCHITECTURE.md` §9.4 Data Classification — remove Biometric row.
- `ARCHITECTURE.md` §9.5 Right to Erasure — remove voiceprint row.
- `ARCHITECTURE.md` §9.8 Voiceprint as Special-Category Biometric
  Data — **delete entirely**.
- `docs/threat-model.md` — update assets, threats, attacker
  profiles.
- `SECURITY.md` — strengthen "no biometric data at all" commitment.
- `docs/adr/0008-monorepo-folder-structure.md` — remove
  `engine_cpp/src/voiceprint/`.
- `docs/adr/0010-cpp-engine-runtime-architecture.md` — update
  ResourceBudget math.
- `proto/aegis/v1/aegis.proto` — remove `AskRAG`; add `is_question`
  to `TranscriptSegment`; clean up voiceprint comments.

## Related

- ADR-0004 Stateless Broadcast Relay (unchanged)
- ADR-0005 Audio Ephemeral Policy (rewritten as part of this
  refactor)
- ADR-0006 Liveness and Disconnect Handling (unchanged)
- ADR-0008 Monorepo Folder Structure (minor update)
- ADR-0010 C++ Engine Runtime Architecture (ResourceBudget math
  update; future-constraint note for AskRAG)
- `ARCHITECTURE.md` §4 Data Flow
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §9 Data Governance & Privacy
- `docs/threat-model.md`
- `SECURITY.md`
