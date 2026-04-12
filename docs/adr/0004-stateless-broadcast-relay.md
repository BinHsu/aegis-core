# ADR-0004: Stateless Broadcast Relay — Server Holds No Meeting Content

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core operates as a **one-host, many-viewer broadcast** system (see
ADR-0001). A single host device (the staff machine) captures audio,
streams it to the Go Gateway, and the Gateway broadcasts speaker-labeled
transcripts and prompter outputs to zero or more viewer devices (the
boss and other observers).

A core architectural question for this topology is: **where does meeting
content live?** Specifically:

1. Does the Go Gateway persist the transcript for replay, late joiners,
   multi-device sync, or recovery?
2. Does the C++ engine retain audio beyond the in-process PCM buffer?
3. Does any durable store (DynamoDB, S3, EBS) ever hold meeting
   transcripts, audio, or derived artifacts?
4. Where is the "source of truth" for a meeting's full history?

This ADR captures the decision: **the server side is stateless with
respect to meeting content. The host device is the only source of truth.
Viewers see a live relay stream and hold no history beyond their rolling
render window.**

## Decision Drivers

- **D1. Privacy posture.** Aegis processes sensitive executive
  conversations. A system that holds no content on the server cannot
  leak content from the server.
- **D2. Subpoena and GDPR surface area.** Content that does not exist
  cannot be handed over in response to legal process. Content that does
  not exist cannot be the subject of a Subject Access Request, a right-
  to-erasure request, or a data breach notification.
- **D3. Operational simplicity.** Stateless services are trivially
  horizontally scalable, trivially observable, and trivially recoverable
  after failure.
- **D4. Multi-tenancy isolation.** Tenants cannot leak content into
  each other's namespaces if there is no content to leak.
- **D5. Development cost.** Persistent transcript storage would demand a
  DynamoDB schema, a KMS-per-tenant encryption story, a TTL retention
  job, a retention policy API, a right-to-erasure API, an audit log for
  content access, and a recovery flow. All of these become unnecessary
  if the server holds no content.

## Considered Options

### Option A — Fully stateless server ✅ chosen

- C++ engine holds audio in process RAM only; discards after inference.
- Go Gateway holds per-session routing state (who is in the meeting) in
  process RAM; holds no transcript content.
- Transcript segments flow: C++ → Go → fanned out to all viewers →
  discarded server-side.
- The **host device** accumulates the full transcript locally (in
  browser memory / IndexedDB for web, or Tauri local storage for a
  future desktop shell).
- Viewers render only what they receive after joining (rolling window).
- Export is the host device's responsibility.

### Option B — Server-side session-scoped store (Redis / in-memory)

- Transcript kept in a Redis-like in-memory store during the meeting.
- Fetched on demand by late joiners, multi-device sync, or recovery.
- TTL purge after meeting end.

### Option C — Durable transcript persistence (DynamoDB / SQL)

- Transcripts written to DynamoDB with per-tenant KMS encryption.
- Configurable retention (30 / 90 / 365 days).
- Enables features like meeting history, cross-meeting search, summary
  generation.

## Decision Outcome

**We choose Option A.**

The server is a **pure relay**:

1. **C++ engine**: keeps audio only in process RAM, scoped to the
   session. On session end (or process exit), all state vanishes.
   Per ADR-0012, the engine does **not** hold voiceprint embeddings of
   any kind.
2. **Go Gateway**: holds a session registry — `session_id → { host
   connection, viewer connection list, created_at, last_host_ping }` —
   and fan-out channels for live transcript segments. No transcript
   content is stored beyond the fan-out buffer (low milliseconds).
3. **Host device**: accumulates full transcript in browser-local memory
   (or Tauri local storage in Phase 4+). Responsible for export.
4. **Viewer devices**: render only what they receive live, in a rolling
   window (e.g., last 5 transcript lines). No server-side replay, no
   client-side persistence in MVP.
5. **Durable stores** (DynamoDB, S3): hold only **tenant metadata**
   (accounts, tenant settings, RAG index pointers). They **never** hold
   meeting transcripts, audio, prompter output, or any derivative of
   real-time meeting content. (Voiceprint data does not appear in this
   list because Aegis does not process it at all — ADR-0012.)

### Why Option A

- **D1 is maximized.** Zero server-side meeting content means zero
  content to leak through server compromise, misconfigured backups, or
  employee access. The blast radius of any future server-side breach is
  limited to tenant metadata and auth records, not meeting content.
- **D2 is dramatically reduced.** GDPR Subject Access Requests for
  meeting content are answered: *"We do not hold meeting content on our
  servers. The data is on the user's device."* Right-to-erasure
  compliance becomes trivial for content (the user clears their own
  device). Legal-hold requests for content must be directed at the end
  user, not us. Breach notification for content becomes structurally
  impossible.
- **D3 is delivered.** Go Gateway replicas are pure fan-out; they can
  scale horizontally by session-affinity routing (per ADR-0001, the same
  session's host and viewers land on the same Go Gateway replica).
  Failure recovery is natural: a lost Go Gateway replica loses its
  active sessions (acknowledged via L2 in ARCHITECTURE §11
  Known Limitations), and new sessions spin up on
  healthy replicas with no consistency work.
- **D4 is automatic.** Tenants cannot cross-contaminate content that
  does not exist. The classical multi-tenancy risk (row-level access
  escape, table scan bug, misconfigured RBAC) is structurally absent.
- **D5 is substantial.** The eliminated components include:
  - DynamoDB transcript schema design, migration, retention jobs.
  - Per-tenant KMS CMK provisioning and rotation.
  - Right-to-erasure API and audit trail.
  - Per-transcript access control list and audit log.
  - PII redaction pipeline in the log / trace layer (because transcript
    content never flows through the log / trace layer at all).

### Why Not Option B (Server-side Session-Scoped Store)

- Addresses enabling late joiners and multi-device sync — but the
  product has decided that **late joiners are deliberately denied
  history** (ARCHITECTURE.md §11 Known Limitation L4; confirmed
  as a feature, not a bug). Removing the late joiner use case removes
  most of Option B's value.
- Reintroduces a content-holding layer on the server, diluting D1, D2,
  and D4. Even session-scoped in-memory state is visible to SRE
  tooling, core dumps, and memory diagnostics — it is not really
  "nothing."
- Adds an operational surface (Redis / in-memory store HA, failure
  modes, memory pressure).
- Value is primarily for multi-device sync for the host — but the host
  is a single device by design (ADR-0001).

### Why Not Option C (Durable Persistence)

- Directly violates D1, D2, D4.
- Multi-week implementation cost (schema, encryption, retention, right-
  to-erasure, audit log) that is avoided entirely by Option A.
- Enables features (cross-meeting history, persistent summaries, RAG
  over past meetings) that are **not on the MVP roadmap** and conflict
  with the product's privacy positioning.
- Could be revisited in a Phase 5+ "Compliance Archival SKU" as an
  **opt-in premium feature** for regulated industries (FINRA, HIPAA).
  That evaluation is out of scope for this ADR.

## Implementation Notes

### Go Gateway Session Registry

The in-memory session registry is the minimal state the Go Gateway holds:

```go
type Session struct {
    ID              string
    CreatedAt       time.Time
    Host            *HostConnection   // WebRTC peer, audio source
    Viewers         map[ViewerID]*ViewerConnection  // fan-out targets
    TranscriptChan  chan TranscriptSegment  // bounded, non-persistent
    TenantID        string
    RAGSelection    string
    LastHostPing    time.Time
}
```

**None of these fields carry transcript content.** `TenantID` is
populated from the host's Cognito JWT in Cloud mode and is empty in
Local mode (per ADR-0007 L7, Local mode has no tenant concept).
`TranscriptChan` is a bounded channel (capacity ~32) used for
fan-out only; segments are written by the ingest side and read by
each viewer goroutine, never
retained.

### Session-Affinity Routing

Per ADR-0001, all participants of a given `session_id` must land on the
same Go Gateway replica. Options for enforcing this in Cloud mode:

- AWS ALB with session cookie stickiness (simplest, works for HTTP/2
  viewers).
- Kubernetes ingress with `nginx.ingress.kubernetes.io/affinity:
  cookie`.
- Client-side routing hint: `CreateMeeting` response includes a short-
  lived URL that pins to a specific replica by DNS or header.

Implementation detail to be decided during Phase 2; not load-bearing
for this ADR.

### No Shared State Between Replicas

Go Gateway replicas **do not share** session registries. A session is
"owned" by exactly one replica. If that replica dies, the session is
terminated (consistent with ARCHITECTURE §11 Known Limitation L2 —
C++ engine crash also terminates the session, so the failure modes are
aligned).

This avoids the need for Redis / etcd / NATS between replicas and
preserves the stateless property.

## Consequences

### Positive

- **Maximum privacy posture.** Content that does not exist cannot leak.
- **Dramatic reduction in compliance burden.** GDPR SAR / right-to-
  erasure / breach notification become structurally easy or absent for
  meeting content.
- **Operational simplicity.** No retention jobs, no encryption key
  rotation for content, no per-tenant content audit log.
- **Phase 2 delivery scope is smaller** — multi-week work on transcript
  persistence is avoided.
- **Tenant isolation is structural**, not policy-based.

### Negative

- **Host crash loses transcript history** (documented as L1 in
  `ARCHITECTURE.md` §11). Mitigation path: optional host-side local
  persistence in a Phase 5+ "recovery" feature, explicitly user-
  controlled.
- **C++ engine crash terminates the meeting** (documented as L2).
  Acceptable because the engine holds audio state that cannot be
  recovered from a restart anyway.
- **No "meeting history" feature.** Users who want to review a meeting
  from last week must have exported it at the time. Phase 3 UX must
  make export prominent and the "unsaved meeting" state clearly visible
  before the user closes their device.
- **No cross-meeting summary or memory.** The RAG corpus (knowledge
  base) is a separate persistent store and is **not** populated from
  meeting transcripts. This is an explicit product-privacy boundary
  (see `ARCHITECTURE.md` §9 Data Governance & Privacy).
- **Failure handling is "fail fast."** A replica loss kills active
  sessions on that replica. Users are told (through UI) to restart the
  meeting. This is consistent with L1 / L2 and avoids the complexity of
  session failover.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers
- ADR-0003 Host Audio Capture Strategy (Pure Web for MVP)
- ADR-0005 Audio Ephemeral Policy
- ADR-0006 Liveness and Disconnect Handling
- `ARCHITECTURE.md` §4 Data Flow
- `ARCHITECTURE.md` §5 Dual-Mode Parity
- `ARCHITECTURE.md` §9 Data Governance & Privacy
- `ARCHITECTURE.md` §11 Known Limitations L1 / L2
