# ADR-0003: Host Audio Capture Strategy — Pure Web for MVP

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —
- **Applies to**: Phase 1–4 (MVP). Phase 4+ may revisit based on user demand
  for native desktop meeting app support.

## Context

The host client (the staff machine, see ADR-0001) must capture a single
mixed audio stream containing all speakers in a meeting and stream it to
the Go Gateway over WebRTC. The stream is consumed by the C++ engine for
transcription and speaker diarization. Because Aegis uses **single-channel
diarization** (see `ARCHITECTURE.md` §4 Data Flow, step 5: "we capture a
single mixed audio track (simplifying hardware), the AI isolates
speakers"), the capture mechanism only needs to deliver "all voices in one
stream" — it does not need per-speaker channel separation.

This decouples capture mechanics from speaker identification: **any
capture method that yields one mixed PCM stream is sufficient**. This is
the insight that unlocks pure-web for the MVP.

This ADR documents how capture happens in the MVP and why the native shell
(Tauri, see ADR-0002) is architecturally reserved but not built in
Phase 3.

## Decision Drivers

- **D1. Ship MVP fast.** Phase 1–4 must deliver a working product without
  building a Rust toolchain and Tauri shell in parallel.
- **D2. Cover the common meeting scenarios on day 1** — physical meeting
  rooms and remote meetings via web-based meeting clients (Zoom Web,
  Google Meet, Teams Web).
- **D3. Preserve the "clone and run" ethos.** No user-side audio driver
  installation, no virtual audio device setup, no per-platform binary in
  the MVP.
- **D4. Keep the Tauri option alive for Phase 4+.** If a material fraction
  of users insist on using native meeting apps (Zoom Desktop, Teams
  Desktop), the Phase 3 frontend must wrap into Tauri without refactor
  (see ADR-0002).
- **D5. Single-channel diarization removes the multi-track argument.** We
  do not need per-speaker audio tracks, so there is no technical reason
  the capture layer must be native.

## Background: What the Browser Can Actually Capture

A common misconception is that browsers cannot capture system audio. This
is partially true but misses the full picture.

Modern browsers expose three audio capture surfaces:

1. **`getUserMedia({audio: true})`** — selected microphone device.
   Universally supported across Chrome, Edge, Firefox, Safari.
2. **`getDisplayMedia({video: true, audio: true})`** — screen / tab
   sharing including an audio track. On Chrome and Edge, the user can
   select a specific browser tab and check **"Share tab audio"**,
   capturing the audio output of that tab. This is the same mechanism
   Google Meet uses for "Present a tab with audio." Firefox support is
   partial; Safari support is incomplete.
3. **`Web Audio API`** — with `MediaStreamAudioSourceNode` and
   `MediaStreamAudioDestinationNode`, the browser can mix multiple
   MediaStream inputs into a single output MediaStream in real time,
   without leaving the browser process.

The practical implication: **if the counterparty runs their meeting in a
browser tab** (Zoom Web, Google Meet, Teams Web), the staff host can
capture that tab's audio via `getDisplayMedia` — no native code, no
virtual audio device, no Tauri required.

The only scenario this does NOT cover is when the counterparty uses a
**native** desktop meeting app whose audio output goes directly to OS-level
sinks that browsers cannot reach. For that scenario, users must either:

- Use the web version of the meeting app instead (documented recommended
  workaround).
- Install a virtual audio device (BlackHole on macOS, VoiceMeeter on
  Windows) that re-exposes system audio as a virtual microphone.
- Wait for Phase 4+ when Tauri provides native system audio access.

## Considered Options

### Option A — Pure web, `getUserMedia` + `getDisplayMedia` + Web Audio mixing ✅ chosen

Capture via the three browser APIs, combined per scenario:

- **Physical conference room**: `getUserMedia({audio: ...})` on the laptop
  microphone.
- **Remote meeting, counterparty in browser**: `getDisplayMedia({video,
  audio: true})` capturing a browser tab (Zoom Web, Meet, Teams Web);
  discard the video track, keep audio.
- **Mix mic + tab audio**: optional toggle. Web Audio API mixes a
  `getUserMedia` mic stream and a `getDisplayMedia` tab stream into one
  output `MediaStreamDestination`.

Send the resulting `MediaStream` to the Go Gateway via `RTCPeerConnection.addTrack()`.

### Option B — Tauri from day 1

Ship Phase 3 as a Tauri app with native CoreAudio / WASAPI capture
alongside the web frontend. Covers native meeting apps on day 1.

### Option C — Pure web with documented virtual audio driver workaround

Pure web, but only `getUserMedia` — no `getDisplayMedia`. Tell users to
install BlackHole / VoiceMeeter and route system audio into a virtual
microphone.

## Decision Outcome

**We choose Option A.**

MVP Phase 3 ships as pure web with a layered capture strategy:

1. **UI mode selector** at meeting start: "Physical room (microphone)" or
   "Remote meeting (capture browser tab)".
2. **Physical room path**: `getUserMedia({audio: { echoCancellation: true,
   noiseSuppression: true, autoGainControl: true }})`.
3. **Remote tab path**: `getDisplayMedia({video: true, audio: true})`;
   discard the video track immediately; keep the audio track.
4. **Optional mix**: "Include my microphone too" toggle. If on, also
   `getUserMedia` and combine via `AudioContext.createMediaStreamSource()`
   → single `MediaStreamDestination`.
5. **Abstraction boundary**: all four of the above are encapsulated in the
   `AudioCaptureProvider` interface required by ADR-0002 Constraint 2, so
   the Phase 4 Tauri backend can replace the implementation without
   touching any call site.

### Why Option A

- **D1**: fastest path to MVP. Single React + Vite codebase in
  TypeScript; no Rust toolchain on the critical path.
- **D2**: covers physical-room AND remote-via-web-meeting-client scenarios
  — the two most common target user journeys.
- **D3**: zero user-side installation beyond "open a Chrome tab." No
  kernel extension, no audio driver, no per-platform binary.
- **D4**: the `AudioCaptureProvider` abstraction keeps the Tauri door open
  for Phase 4 without costing any Phase 3 velocity.
- **D5**: single-channel diarization means we do not need the multi-track
  capture machinery that would otherwise push toward native code.

### Why Not Option B (Tauri day 1)

- Violates **D1**. Adds Rust toolchain, Tauri build pipeline, CoreAudio
  and WASAPI glue, cross-platform binary packaging — multi-week work that
  delays MVP delivery with no proportional benefit.
- Optimizes for a scenario (native desktop meeting app) that has **not
  been validated** as a real MVP customer need. Premature optimization.
- Phase 3 → Phase 4 reuse (per ADR-0002) means no rework penalty for
  deferring Tauri — the web frontend will be wrappable when we do build
  the shell.

### Why Not Option C (virtual audio driver workaround)

- Violates **D3** significantly. Installing kernel-level audio drivers
  (BlackHole, VoiceMeeter) is the antithesis of "clone and run." A
  tech-savvy individual can do it; the target enterprise staff user
  typically cannot without IT support.
- Violates **D2** for remote meetings where we already have a better
  answer (`getDisplayMedia` tab audio) that requires no installation.
- Reserved as a **documented fallback** for power users who need to
  capture native-app audio before Phase 4 ships. Not the default flow.

## Capture Flow (MVP, Remote Meeting Example)

1. Staff opens Aegis in Chrome or Edge.
2. Staff opens Zoom Web / Google Meet / Teams Web in a separate tab.
3. Staff presses "New Meeting" in Aegis. UI asks: "Where is the audio
   coming from?" with options "Another browser tab" and "My microphone."
4. Staff picks "Another browser tab" → Aegis calls
   `getDisplayMedia({video: true, audio: true})`.
5. Chrome shows its native tab picker. Staff selects the meeting tab and
   checks "Share tab audio."
6. Aegis receives a `MediaStream` with one audio track and one video
   track; it **immediately discards the video track** and keeps only
   audio.
7. The audio track is passed to `RTCPeerConnection.addTrack()`. WebRTC
   negotiates with Go Gateway. PCM flows.
8. C++ engine receives PCM, performs diarization, emits speaker-labeled
   transcript segments. Go Gateway broadcasts to viewers (per ADR-0004).

For physical-room meetings, steps 2–6 collapse to a single `getUserMedia`
call on the laptop microphone.

## Constraints and Caveats

- **Browser support is Chrome / Edge only (MVP).** `getDisplayMedia`
  tab-audio capture is fully supported on Chrome 74+ and Edge Chromium.
  Firefox has partial support and its tab-audio path is unreliable.
  Safari support is incomplete. **MVP supported browsers: Chrome and
  Edge.** This must be documented in `README.md`. Viewer role works on
  any modern browser regardless.
- **HTTPS / secure context requirement.** Both `getUserMedia` and
  `getDisplayMedia` require a secure context.
  - In Cloud mode: Go Gateway serves the frontend over TLS (ACM cert via
    the `aegis-aws-landing-zone` infra repository).
  - In Local mode: the frontend is loaded from `http://localhost:PORT`.
    `localhost` is exempt from the secure-context requirement by browser
    specification, so capture works without a self-signed cert for the
    host device. LAN viewers (see ADR-0007) do not need
    `getUserMedia` and so are not blocked by the absence of TLS.
- **Per-session user prompt.** `getDisplayMedia` prompts the user to pick
  a tab at every meeting start. Friction: ~5 seconds per meeting. UI must
  make clear that **only audio is captured**, not screen contents:
  > "We use this prompt to capture the audio from your meeting tab.
  > Screen contents are discarded and never sent anywhere."
- **Acoustic echo on physical-room laptop mic.** When laptop speakers
  play remote audio and the same laptop mic captures the room, there is
  acoustic echo. Mitigations:
  - `getUserMedia`'s built-in `echoCancellation: true` handles
    moderate echo.
  - Recommend headphones for the staff in all-remote scenarios (will be
    documented as a usage tip, not enforced).
- **Sample rate.** Browsers typically deliver 48 kHz; `whisper.cpp` is
  tuned for 16 kHz input. MVP choice: let the **C++ engine** downsample
  on ingest. This keeps the browser layer simple and avoids per-browser
  AudioContext behavior quirks.
- **Discarding `getDisplayMedia` video track user anxiety.** Even with
  clear UI copy, some users will worry that screen contents are being
  transmitted. Mitigation: (a) in-product copy, (b) an in-product
  "Privacy" link that explains exactly what is captured and what is not.

## Consequences

### Positive

- Phase 3 ships as a single React + Vite project. No Rust, no Tauri, no
  cross-platform binaries.
- Covers the two most common user journeys on day 1 (physical room,
  remote via web meeting client).
- Aligns with ADR-0002's deferred Tauri decision and preserves the
  Phase 4 migration path.
- Zero user-side installation.
- Keeps the MVP threat surface small: no native code running with
  elevated OS audio privileges.

### Negative

- **Users on native Zoom Desktop / Teams Desktop are not supported on
  day 1.** Workaround: ask them to use the web client of those services.
  Documented as a known limitation; revisit in Phase 4 based on user
  feedback.
- **Chrome / Edge only for host role in MVP.** Firefox and Safari users
  cannot create meetings until browser parity improves or Tauri ships.
  Viewer role is unaffected.
- **Per-session permission friction.** Users must pick a tab and check
  "Share tab audio" every meeting. Acceptable but not frictionless.
- **Echo mitigation is best-effort.** We rely on the browser's AEC, which
  is good but not perfect. Users in noisy rooms may see degraded
  transcription quality.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers
- ADR-0002 Desktop Shell Technology — Tauri over Qt / Electron
  (Constraint 2: `AudioCaptureProvider` abstraction)
- ADR-0004 Stateless Broadcast Relay
- ADR-0007 Local Mode LAN Topology
- `ARCHITECTURE.md` §4 Data Flow (single-channel diarization)
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `README.md` — "Clone it, build it, it just works" ethos
