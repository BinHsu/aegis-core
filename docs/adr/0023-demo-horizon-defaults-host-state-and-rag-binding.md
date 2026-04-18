# ADR-0023: Demo-horizon defaults — host state persistence + RAG binding

| Field    | Value |
| -------- | ----- |
| Status   | Accepted (2026-04-18) for the demo horizon; Phase 4 re-evaluation triggers below |
| Date     | 2026-04-18 |
| Deciders | Project author |
| Supersedes | — |
| Superseded by | — |

## Context

Phase 3c Slice 2 had to make two small design calls that each have a
bigger shadow than their LOC count suggests. Rather than resolving
them silently in code, this ADR records both so Phase 4 does not
relitigate either under deadline pressure.

### Decision A — Host-side session state persistence

**What**: the Host page's auth principal + active-meeting state lives
in React reducer state only. No `sessionStorage`, no `localStorage`,
no HttpOnly cookie. A page refresh returns the host to the
`signed-out` branch and — if a meeting was active — ends the
meeting server-side (ICE disconnect on the gateway's side triggers
the graceful-shutdown path documented in ADR-0006).

**Why not sessionStorage / localStorage / cookie today**:

- **sessionStorage (~30 LOC, 1 hour)**: solves refresh persistence
  per-tab but adds XSS exposure for tokens and is still wiped on tab
  close. Marginal UX win over in-memory.
- **localStorage**: same XSS exposure, longer lifetime — larger
  blast radius. Same marginal UX over sessionStorage.
- **HttpOnly Secure cookie + `GetMe` RPC (1–2 days)**: the secure
  answer, but pairing it to the current `StaticJWTProvider` stub
  wastes work that a Phase 4 Cognito wiring will redo. CORS,
  cross-origin `SameSite`, CSRF strategy, dev-mode Vite proxy all
  have to be designed now against a backend that does not yet exist.

**Product argument**: the chief-of-staff is *actively driving* the
meeting. If they refresh mid-meeting, the WebRTC connection and
audio-capture `MediaStream` are gone regardless of token survival
— the meeting has already ended for operational reasons. Adding
state persistence only solves "restore the meeting form inputs after
an accidental refresh before capture started," which is a marginal
benefit not worth the XSS surface of JS-accessible storage.

**Accepted consequence**: a host refresh loses everything. We treat
this as the product contract, not a bug.

### Decision B — RAG corpus binding is opt-in

**What**: `CreateMeetingRequest.rag_id` accepts an empty string,
meaning "no RAG binding — transcript-only meeting; staff provides
hints manually". Gateway must NOT reject empty rag_id with
`INVALID_ARGUMENT` / `NOT_FOUND`. Engine session start must tolerate
empty rag_id (skip any RAG init when the query path lands in
Phase 4+). Frontend `HostPage` dropdown defaults to "(No corpus)".

**Why opt-in**:

- Plenty of meetings are better served by the chief-of-staff's own
  judgement than by a mediocre retrieval hit. A forced default
  corpus trains the user to ignore retrieval quality rather than
  notice when it helps.
- Every corpus binding carries a non-zero engine-side cost once the
  query path lives (embedder warmup, Qdrant socket, HNSW index
  pages in cache). Opting in means meetings that do not want RAG do
  not pay for RAG.
- It sharpens the product framing: Aegis's core promise is
  real-time transcription + prompter; RAG is a premium augment.

**Accepted consequence**: the Host UI must make the "no corpus"
choice visible and non-punishing. Default dropdown text is
`(No corpus — staff provides hints manually)`, not `Please select…`.

## Decision

Ship both A and B in Phase 3c Slice 2. Code changes in this ADR's
landing PR:

- `proto/aegis/v1/aegis.proto` — `rag_id` docstring explicitly
  permits empty string; cross-references this ADR.
- `gateway_go/internal/grpc/gateway_service.go` —
  `CreateMeeting` drops the empty-rag_id `INVALID_ARGUMENT` branch.
- `gateway_go/internal/grpc/gateway_service_test.go` — rename
  `TestCreateMeetingRejectsEmptyRag` → `TestCreateMeetingAcceptsEmptyRag`
  and flip the assertion.
- `frontend_web/src/pages/Host/HostPage.tsx` — `RAG_CORPORA`
  gains `{ value: "", label: "(No corpus — staff provides hints
  manually)" }` as the first entry; `DEFAULT_RAG_ID = ""`; also
  align `TRANSCRIPT_TAIL` (8 → 5) with Viewer's `PROMPTER_WINDOW`.

No engine-side changes today — the RAG query path does not exist
yet, so "empty rag_id skips RAG init" is trivially satisfied. The
proto docstring on `StreamStart.rag_id` (`aegis.proto:461`) is the
contract the engine must honor when query lands.

## Phase 4 re-evaluation triggers

Either decision SHOULD be revisited when:

1. **Cognito JWT middleware goes live on the gateway.** ADR-0001
   precondition. At that point a real long-lived session exists,
   and HttpOnly cookies become the right shape rather than a
   pre-mature commit. Bundle A's migration with that PR.
2. **Multi-host meeting mode ships.** If two staff share a single
   meeting (delegation, shift handover), in-memory state breaks the
   UX. Decision A's in-memory posture then stops fitting.
3. **A customer contract requires session recovery SLA.** Explicit
   promise of "refresh survives" flips Decision A.
4. **Query path latency drives a corpus pre-load default.** If
   bge-m3 embedder warmup becomes the critical-path meeting-start
   latency, a corpus preselection might be worth re-introducing —
   but as a per-tenant preference, not a global default.

None of the four triggers is close today. This ADR is the written
commitment that neither decision is re-opened under deadline
pressure before one of them fires.

## Consequences

### Positive

- **Tiny code surface.** Both decisions land in Slice 2's budget
  (~60 LOC + this ADR). No proto-wire changes, no CORS reshuffle.
- **Consistent decision posture.** Same two-phase pattern as
  ADR-0014 (β demo / δ production) and ADR-0022 (Phase 4 Cognito
  multi-tenancy) — demo-horizon simplification + Phase 4 migration
  trigger — makes the project's overall rhythm predictable.
- **Honest product framing.** "No RAG is a first-class mode" is a
  more truthful statement of what Aegis actually offers in Phase 3
  than "RAG is the point and you must pick a corpus."

### Negative

- **Refresh UX regression on paper.** A reviewer reading the code
  without this ADR may think we forgot to persist the session.
  This ADR + the docstring in `HostPage.tsx` are the cure.
- **Two Phase 4 migrations to schedule.** When Cognito lands, A's
  cookie migration is one item; any query-path corpus-preference
  work is another. Both are small individually but need to appear
  on the Phase 4 burn-down.

### Risks

- **Customer demo of "I refreshed and lost everything" landing
  badly.** Mitigation: the Host UI should surface a
  "Meeting ended due to page refresh — start a new one" message
  when a fresh load finds a recent meeting in localStorage
  (hm — but we committed to no localStorage). Better mitigation:
  documentation in user-facing help text explaining the contract.
  Acceptable since the demo audience is internal.

## Related

- ADR-0001 — CreateMeeting + session-token issuance (the auth
  context Decision A persists in memory).
- ADR-0006 — ICE Consent Freshness + 4-hour graceful drain (what
  happens server-side when the host refreshes).
- ADR-0014 — Bazel build cache strategy (β demo / δ production —
  same two-phase pattern this ADR inherits).
- ADR-0020 — Engine owns inference (why "skip RAG init" is a
  single-binary concern, not a microservice one).
- ADR-0022 — Cloud multi-tenancy isolation (Phase 4 companion that
  will bundle this ADR's Decision A migration).
