# ADR-0001: Session Join Mechanism for Meeting Viewers

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core uses a **one-host, many-viewer broadcast** model. A meeting has a
single host (the staff machine, which captures audio and drives inference) and
zero or more viewers (the boss, additional staff, or other observers). See
`ARCHITECTURE.md` §4 Data Flow for the full data path.

Viewers need a mechanism to join an active meeting. We must decide:

1. Does a viewer need to authenticate with an account to join?
2. How does a viewer discover the meeting they are allowed to see?
3. How are unauthorized viewers prevented from eavesdropping on prompter
   content?

This decision affects the Go Gateway's session management, the authorization
middleware, the Cognito JWT path (Cloud) and its Local-mode fallback, the
client routing/login flow, and — most importantly — the boss's end-user
experience, which must remain frictionless.

## Decision Drivers

- **D1. Boss-viewer UX must be frictionless.** The boss in our target market
  is a senior executive with low tolerance for installing apps, managing
  credentials, or navigating dashboards. "Click a link, see the prompter" is
  the ideal experience.
- **D2. Privacy posture must stay strong.** Prompter content can contain
  sensitive real-time tactical information; unauthorized access is a serious
  breach.
- **D3. MVP implementation cost must be minimized.** Phase 1–4 needs to ship;
  enterprise-grade access control is scope creep at this stage.
- **D4. The design must not foreclose future per-account access control.**
  Enterprise customers (Phase 5+) will eventually want "only these specific
  accounts can join this meeting." The MVP design must extend to that case
  without breaking changes.
- **D5. Credential blast radius must be bounded in time.** Because the server
  stores no meeting content (see `ARCHITECTURE.md` §9 Data Governance
  & Privacy), any session credential automatically ceases to be useful when
  the session ends.

## Considered Options

### Option A — Account-based access control

The viewer logs into Aegis with their own Cognito account. A dashboard lists
meetings they have been granted access to. The host, when creating a meeting,
explicitly picks allowed accounts.

- Each incoming viewer connection is authenticated against an allowlist.
- Requires per-tenant RBAC, account provisioning, dashboard UI, allowlist
  backend.

### Option B — Invite link / token based access  ✅ chosen

The host creates a meeting. The Go Gateway returns an opaque `session_id` and
a short-lived JWT join token. The host shares a URL of the form

```
https://aegis.example.com/view/<session_id>?token=<jwt>
```

over a secure out-of-band channel (corporate IM, iMessage, Teams DM, etc.).

- Any client opening the URL is a valid viewer for the duration of that
  session.
- The token is cryptographically bound to the session ID and expires with the
  session (plus a small grace period).
- No viewer-side account or login is required.

### Option C — Pre-bound viewer pairing

A persistent pairing relationship between a staff account and a boss account.
When the staff starts a meeting, the boss's app/device is automatically
notified and can join without any link-sharing.

- Requires persistent pairing state per tenant.
- Requires push notification or long-polling channel for boss notification.
- Still needs fallback machinery for one-off guests.

## Decision Outcome

**We choose Option B (invite link / token based access) for MVP (Phase 1–4).**

Implementation requirements:

1. `CreateMeeting` gRPC returns `{ session_id, viewer_join_token }`.
2. The viewer join endpoint accepts the token via URL query parameter (for
   link sharing) or HTTP header (for programmatic clients).
3. Token validation is a **first-class middleware** in the Go Gateway,
   decoupled from business logic, so Option A's allowlist check can be layered
   on later as additional middleware without restructuring.
4. Tokens are short-lived JWTs signed by a rotating server-side key.
   Recommended defaults: `exp = session_max_lifetime + 10min grace`,
   `session_max_lifetime = 4h` (tunable per tenant).
5. In Local mode, the token is still issued and validated. The
   signing key is a process-scoped random key generated at startup,
   consistent with `ARCHITECTURE.md` §5 "Local Mode Interface
   Fallback"; there is no persistent identity store in Local mode.
   For LAN binding, viewer transport, and QR code discovery
   details, see **ADR-0007 Local Mode LAN Topology**.

### Why Option B

- **D1 wins decisively.** Boss opens a URL on any device (laptop, tablet,
  phone, spare monitor) and sees the prompter. Zero install, zero account
  setup, zero training.
- **D3 is satisfied.** JWT signing and verification are standard middleware
  in Go; no dashboard, no user management, no allowlist storage for MVP.
- **D5 is naturally satisfied.** Token expires with the session; no
  historical content exists to be replayed later even if a URL leaks.
- **D2 is adequately satisfied for MVP.** The security model is "whoever has
  the link can view" — equivalent to Google Meet, Zoom, and most SaaS meeting
  tools. Acceptable under the MVP threat model.
- **D4 is preserved** (see Future Extension Path below).

### Why Not Option A (for MVP)

- Violates **D1** directly: account login, dashboards, and credential
  management are exactly the friction we want to eliminate for the boss.
- Violates **D3**: multi-week implementation cost for machinery that may not
  match enterprise needs once we learn them.
- Correct for the enterprise tier later; premature now.

### Why Not Option C

- Violates **D3** significantly: persistent pairing state, notification
  infrastructure, a different auth UX for boss.
- Only partially addresses **D4**: pairing is a narrower model than general
  allowlisting; Option A's machinery would still be needed for enterprise use
  cases.
- Best positioned as a Phase 5+ ergonomic enhancement on top of Option A, not
  a replacement for Option B.

## Future Extension Path

Option A can be added later **without breaking changes**:

1. Add an optional field to `CreateMeetingRequest`:
   ```proto
   message CreateMeetingRequest {
     string rag_id = 1;
     string title = 2;
     // Added later: if non-empty, only these accounts may use the join token.
     repeated string allowed_viewer_account_ids = 3;
   }
   ```
2. Add a second middleware on the viewer join endpoint that, *when*
   `allowed_viewer_account_ids` is non-empty on the session, requires the
   viewer to present **both** the session token AND a valid Cognito JWT
   matching an allowed account.
3. Existing clients that omit `allowed_viewer_account_ids` continue to work
   unchanged (open-link access).

This preserves full backward compatibility and lets enterprise customers opt
into stricter access control when needed.

Option C can later be layered on top of a matured Option A by persisting a
"default `allowed_viewer_account_ids`" at the pairing-relationship level.

## Consequences

### Positive

- Boss-viewer experience is as simple as opening a URL.
- Zero account-management cost for MVP.
- Implementation fits within Phase 2 (Internal MVP & BFF).
- Extensible to Option A without breaking changes.
- Consistent auth contract across Local and Cloud modes.

### Negative

- **Link leakage equals viewer leakage.** A staff member forwarding a join URL
  to the wrong person leaks viewer access until the session ends. Mitigations:
  short token lifetime, absence of historical server-side content, in-product
  guidance on secure sharing.
- **No per-viewer audit trail in MVP.** We cannot answer "who viewed this
  meeting?" because viewers are anonymous. This gap closes when Option A lands
  for the enterprise tier.
- **Depends on secure out-of-band sharing.** If staff pastes a join URL into a
  public channel (wrong Slack room, email CC slip), the link is exposed.
  Mitigation is process/training, not technical.

## Related

- `ARCHITECTURE.md` §4 Data Flow
- `ARCHITECTURE.md` §5 Dual-Mode Parity
- `ARCHITECTURE.md` §8 Enterprise Standards (Identity First)
- ADR-0004 Stateless Broadcast Relay
- ADR-0005 Audio Ephemeral Policy
- ADR-0006 Liveness and Disconnect Handling
