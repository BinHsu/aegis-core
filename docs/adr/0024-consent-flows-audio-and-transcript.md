# ADR-0024: Consent flows — audio-processing (one-time) + transcript (two-phase)

| Field    | Value |
| -------- | ----- |
| Status   | Accepted (2026-04-18) for Phase 3c frontend; consent-ledger persistence deferred to Phase 4 (DynamoDB wiring) |
| Date     | 2026-04-18 |
| Deciders | Project author |
| Supersedes | — |
| Superseded by | — |

## Context

Aegis processes meeting audio in real time and produces transcripts
that are *legally and ethically sensitive*: a participant's spoken
words are PII under GDPR Art. 4(1), potentially Art. 9 special
categories if health / political / religious content surfaces, and
are a live target of BIPA / CCPA / state wiretap statutes across
jurisdictions. ADR-0012 already removed the biometric-processing
exposure by eliminating voiceprint matching, but the remaining
surface — "the host can see a transcript on their screen and export
it" — still requires a thought-through consent story.

ARCHITECTURE.md §9.3 already specifies a one-time audio-processing
consent captured at first use. This ADR extends that into a concrete
three-part consent design for the Phase 3c Host UI:

1. **Audio-processing consent** (ARCH §9.3 mandate): first use, once
   per account per privacy-policy version.
2. **Transcript panel visibility** (new): per-meeting opt-in for the
   host to see the transcript on-screen during the meeting.
3. **Transcript export**: per-export opt-in modal acknowledging
   responsibility transfer when the host saves the transcript out.

The third is operationally triggered by the export flow (Slice 5)
but the modal component + audit emission live here (Slice 3) because
they are consent infrastructure, not export infrastructure.

## Decisions

### Decision A — Audio-processing consent is a one-time, localStorage-persisted gate

On the host's first visit to the authenticated Host page, a modal
appears with the ARCH §9.3 copy verbatim:

> *"Aegis transcribes your meeting audio in real time and generates
> suggestion hints from a knowledge corpus you select. Audio is
> processed only in memory and is never saved."*

Only an `[Agree]` action is offered; there is no "decline" path —
the app is unusable without this consent, so the honest UX is to
refuse further interaction rather than paint a disabled shell.

Upon acceptance, a consent record writes to `localStorage` keyed by
policy version:

```
localStorage["aegis.consent.audio_processing.v1"] =
  JSON.stringify({ accepted_at: <ISO 8601>, user_id: <sub>,
                   policy_version: "v1" })
```

The `v1` segment of the key is load-bearing — if the policy copy
changes materially (a new privacy-policy version), the `v2` key
absence will re-prompt the user for explicit consent to the new
terms. This is the "per privacy-policy-version" rule from ARCH §9.3.

**Phase 4 migration**: the client-side localStorage record is a
demo-horizon convenience. The ARCH §9.3-mandated consent ledger
(persistent, append-only, 7-year retention) lives server-side in
DynamoDB (Cloud) / SQLite (Local) and is wired in Phase 4 when the
gateway learns to persist consent entries. The Phase 3 frontend
emits the same consent-record shape via `console.info` so the Slice
3 code path is compatible with a Phase 4 gateway RPC drop-in.

### Decision B — Transcript panel is opt-in per meeting, default OFF

The Host page's New Meeting form gains a toggle:

> `[ ] Show live transcript on this screen`

Default **OFF**. When the host toggles it ON, an inline notice
appears below the toggle:

> *Turning on the live transcript shows meeting content on your
> screen. Aegis processes this data under GDPR Art. 6(1)(f)
> (legitimate interests — operating the service you requested) and,
> where participants' messages include special-category data, Art.
> 9(2)(a) (your explicit consent to see it). You are responsible
> for the physical security of your screen (bystanders, recording
> devices) while the panel is visible.*

The toggle state is passed to the component as a client-side flag
only — it does NOT go over the wire to the Gateway. The backend
keeps transcribing regardless; this decision is purely about what
the host's browser RENDERS. If the host toggles OFF mid-meeting, the
transcript div becomes a "Transcript display disabled — toggle on
in the meeting form to view" placeholder.

**Why default OFF:** privacy-preserving default. The chief-of-staff
is *running* the meeting; they don't inherently need to watch the
transcript. Opt-in acknowledges the screen-as-attack-surface risk
(over-shoulder viewing, screenshots, accidental screen-share
exposure).

### Decision C — Transcript export requires phase-2 confirmation + audit log

When the host initiates an export (Slice 5 wires the button), a
modal appears before the save:

> *You are about to save the meeting transcript to a file. By
> proceeding, you confirm that:*
>
> - *You are responsible for the transcript file under your
>   jurisdiction's data-protection laws from this moment forward.*
> - *You will not share the file with anyone not authorized to see
>   the meeting's contents.*
> - *This action is recorded in the consent ledger with your user
>   ID, session ID, timestamp, and client metadata.*
>
> `[Confirm export]` `[Cancel]`

On confirm, the frontend:
1. Writes an audit record with shape:
   ```
   { kind: "consent:transcript:export:confirmed",
     user_id, session_id, exported_at: <ISO 8601>,
     export_format: "markdown" | "json",
     client_metadata: { user_agent, deploy_mode } }
   ```
2. In Phase 3: emits via `console.info` (no gateway wiring).
3. In Phase 4: POSTs to a `LogConsentEvent` gateway RPC that
   writes the DynamoDB consent-ledger row.

The modal is a distinct, reusable component (`TranscriptExportConsentModal`).
Slice 3 builds + exports it; Slice 5's export handler wires it in.

### Decision D — NO `user-select: none` on transcript text

Emphatic: transcript text **must remain selectable**. Reasoning:

1. **Screenshots bypass it entirely.** Any attacker with a camera
   or OS-level screenshot capability captures the text regardless
   of `user-select`; the CSS rule's protection is theatrical.
2. **It breaks screen readers and accessibility.** Sighted users
   can still work around disabled selection (browser extensions,
   right-click → Save As); screen-reader users cannot.
3. **It signals a defense posture the app cannot deliver.** A user
   who sees "I can't select this" assumes the text is protected —
   but an actual leak-via-screenshot remains trivial. The honest
   posture is to keep selection on and, if traceability matters,
   layer on the watermark (Decision E).

The Tauri-compliance script
(`tools/scripts/check_frontend_tauri_compliance.sh`) is extended in
Slice 3 to grep for `user-select:\s*none` and the React equivalent
`userSelect:\s*["']none["']`; any occurrence fails CI.

### Decision E — Watermarking is an optional Phase 4+ mitigation

If operational experience shows transcript leak traceability is
worth the visual cost, the Host page can layer a low-contrast
watermark over the transcript pane containing:

```
<user_id> · <session_id short hash> · <timestamp YYYY-MM-DD HH:MM>
```

Attributes:
- CSS `position: absolute`, opacity ≈ 0.08–0.12
- Repeated diagonal pattern (not just a single corner mark —
  screenshot-cropping should be expensive)
- Color that has contrast 3–4:1 against transcript text so it
  survives grayscale / print

This is **not** implemented in Phase 3. It's documented here so the
future adopter does not re-derive the design under pressure. The
trigger to implement is a customer request for leak traceability,
or an internal incident where leaked transcript would have been
trackable.

## Phase 4 triggers

- Cognito JWT middleware wires in → consent ledger moves from
  client-side localStorage + console.info to a real gateway RPC
  persisting DynamoDB consent rows. Both A and C migrate.
- First customer requires leak traceability → implement Decision E
  watermarking.
- DynamoDB consent-ledger table provisioned by ldz (per this repo's
  standing #11 IRSA role future requirements, added 2026-04-18).

## Consequences

### Positive

- **Consent surface is a contract, not vibes.** Every consent
  gate has a written trigger, written copy, and a specified audit
  shape. A reviewer can trace each gate back to ARCH §9.3 or
  GDPR / BIPA language without re-deriving.
- **Screenshots stay usable as debugging aids.** A supportive
  accessibility posture + a realistic threat model together say
  "selection stays on; watermark if you need traceability."
- **Phase 3 / Phase 4 migration is small.** The consent-record
  shape emitted in Phase 3 (`{user_id, session_id, timestamp,
  client_metadata, kind}`) matches the future DynamoDB row
  schema. Gateway RPC is a drop-in.

### Negative

- **First-use modal adds friction.** One extra click on the very
  first Host page visit. Mitigated by the one-time nature (localStorage
  persists) and the non-scary copy.
- **Transcript-panel-OFF default feels surprising.** Users who
  *want* to watch the live transcript must opt in every meeting.
  Acceptable: the toggle is prominent in the form, not buried.

### Risks

- **localStorage can be wiped.** Browser clear-site-data, private
  browsing mode, or user cookie-cleaners delete the audio-
  processing consent record → user sees the modal again. This is
  correct behavior: the consent was scoped to this localStorage,
  and the localStorage went away. No data integrity risk; only a
  small UX nuisance.
- **Phase 4 consent ledger migration writes nothing pre-Phase 4.**
  The 7-year audit retention clock does NOT start until Phase 4.
  Demos and internal use run with only the localStorage record —
  acceptable for demo horizon, blocker for any customer onboarding
  where auditable consent trail is a contract requirement.

## Related

- ARCHITECTURE.md §9.3 Consent Capture — the mandate this ADR
  concretizes into Phase 3c code.
- ADR-0012 Remove voiceprint matching — why there is no biometric
  consent; this ADR handles only audio-processing + transcript
  consent.
- ADR-0022 Cloud multi-tenancy isolation — Phase 4 sibling, same
  Cognito-wiring trigger.
- ADR-0023 Demo-horizon defaults — same two-phase-decision pattern
  (demo now + Phase 4 migration trigger).
- GDPR Art. 6(1)(f), Art. 9(2)(a), Art. 4(1).
- BIPA (Illinois), CCPA (California), state wiretap statutes —
  jurisdictional surface the export consent modal transfers
  responsibility for.
