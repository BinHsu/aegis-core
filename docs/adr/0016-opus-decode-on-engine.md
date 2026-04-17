# ADR-0016: Opus decode moves from the Gateway to the Engine

| Field    | Value                                                                   |
| -------- | ----------------------------------------------------------------------- |
| Status   | Accepted — implementation in progress (see checklist at bottom)         |
| Date     | 2026-04-14                                                              |
| Deciders | Project author                                                          |
| Context  | Phase 3 live-test discovery: `pion/opus` rejects real WebRTC audio from iOS Safari / Chrome with `unsupported configuration mode: 3`. Triggered a domain-boundary review of where codec work belongs. |
| Related  | ADR-0004 (stateless relay), ADR-0005 (audio ephemeral policy), ADR-0007 (LAN topology), ADR-0009 (C++ build + whisper.cpp), ADR-0010 (engine runtime architecture), ADR-0013 (proto codegen), ADR-0017 (gateway-engine topology), ADR-0018 (language choice rationale) |

## Context

Phase 2 put Opus decoding inside the Go gateway: the WebRTC ingress
loop in `gateway_go/internal/pipeline/pipeline.go` pulled Opus frames
off the RTP track, fed them to `github.com/pion/opus`, and forwarded
16-bit PCM to the engine over `StreamTranscribe.PcmChunk`. On the
LAN-phone demo path (iOS Safari → gateway → engine), the pipeline
spammed the following log line, once per 20 ms audio frame:

```
pipeline: opus decode: unsupported configuration mode: 3
```

Root cause: `pion/opus` is a pure-Go Opus implementation that has
**not** reached coverage parity with `libopus`. RFC 6716 defines
three coding modes — SILK (low-rate speech), CELT (music), and
Hybrid (both stacked). WebRTC browsers routinely negotiate Hybrid
(`config` field value 14–15, which maps to the "mode 3" error
surface in pion). `pion/opus` refuses to decode these frames,
returning the error verbatim for every single 20 ms slice.

Three options were on the table:

| Option | Where Opus decodes | Notes                                     |
| ------ | ------------------ | ----------------------------------------- |
| A      | Gateway (Go)       | Keep pion/opus; wait for upstream or fork. |
| A'     | Gateway (Go)       | Switch to cgo-wrapped libopus in Go.      |
| **C**  | **Engine (C++)**   | Forward raw Opus frames verbatim; decode next to whisper. |

**Option C was chosen.** The rest of this doc argues why the
domain-correct answer overrides the "smallest-diff" answer (A / A').

## Decision

**The Gateway stops decoding Opus.** It peels the RTP header off
WebRTC audio packets and forwards the Opus payload verbatim to the
engine as a new proto variant `IngestMessage.opus` (`OpusChunk`).
**The Engine** owns decoding: a new `aegis::audio::OpusDecoder` class
wraps `libopus` and emits 16 kHz mono float32 PCM directly into
`whisper_full`.

Codec work — including decode, PLC, jitter, sample-rate conversion —
is part of the **audio processing domain**, not the **session
transport domain**. The Gateway's job is authentication, session
registry, WebRTC negotiation, fan-out. Once it has a byte string
that it knows is an Opus frame, its work there is done.

### Proto layer

`proto/aegis/v1/aegis.proto` gains:

- A new variant `OpusChunk opus = 4` inside
  `IngestMessage.oneof payload` — backwards-compatible addition per
  proto3 rules.
- A new `OpusChunk` message:
  ```proto
  message OpusChunk {
    bytes  opus      = 1;  // raw Opus frame, <= 1275 bytes (RFC 6716 §3.2)
    uint64 chunk_id  = 2;  // monotonic sequence number, 0-based
    int64  offset_ms = 3;  // wall-clock offset from session start
  }
  ```
- A comment block reaffirming the ADR-0005 R3 privacy posture: Opus
  is lossy but reversible to a voice signal, therefore sensitive
  audio. Gateway's small audit surface (single `WriteRTPPayload`
  call site) defers wrapper-type enforcement to Semgrep — see the
  proto comment for the rationale.

The `PcmChunk` variant stays — WAV fixture replay (integration
tests) and future push-to-talk WebSocket sources still feed raw PCM
directly.

### Gateway layer

`gateway_go/internal/pipeline/pipeline.go`:

- Remove the pion/opus import and its decoder instance.
- The RTP read loop now calls `stream.Send(&IngestMessage{
  Payload: &IngestMessage_Opus{Opus: &OpusChunk{Opus: rtpPkt.Payload, …}}})`.
- `chunk_id` monotonically increases from 0 for each new session.
  `offset_ms` is derived from the first frame's wallclock.
- `go.mod` drops `github.com/pion/opus`. `MODULE.bazel`'s
  `use_repo(go_deps, …)` list drops the pion/opus repo.

`gateway_go/internal/pipeline/pipeline_test.go`:

- bufconn stub expectations change from `PcmChunk` to `OpusChunk`.
- Test fixtures no longer need a decoded-PCM golden — they just
  verify the Opus bytes are forwarded intact and the chunk metadata
  is correct.

### Engine layer

`engine_cpp/src/audio/`:

- New class `aegis::audio::OpusDecoder` (`opus_decoder.h` / `.cc`).
  Thin wrapper over libopus's C API. One instance per session
  (matches the existing "one whisper context per session" model).
  Emits 16 kHz mono float32 PCM — zero intermediate int16 step.
- `BUILD.bazel` adds the `opus_decoder` target depending on
  `//engine_cpp/third_party/libopus:libopus`.

`engine_cpp/src/grpc/aegis_engine_service.cc`:

- The StreamTranscribe handler's switch on `IngestMessage::payload_case`
  gains an `kOpus` branch.
- Per-session state struct grows a `std::unique_ptr<audio::OpusDecoder>`
  alongside the existing `whisper_engine`.
- The `kOpus` branch: `decoder->Decode(opus_bytes)` → pass the
  resulting float span to `whisper_full` the same way the existing
  `kPcm` branch does after its int16 → float32 conversion.

### Build-system layer

`MODULE.bazel`:

- New `http_archive(name = "libopus", urls = …, sha256 = …)` pinned
  to Opus 1.5.2 tarball. Same `rules_foreign_cc` + `cmake` pattern
  as whisper.cpp (ADR-0009).

`engine_cpp/third_party/libopus/`:

- `libopus.BUILD` — `cmake()` rule with `BUILD_SHARED_LIBS=OFF`,
  `OPUS_BUILD_TESTING=OFF`, `OPUS_BUILD_PROGRAMS=OFF`. Produces
  `libopus.a`.
- `BUILD.bazel` — alias `:libopus` → `@libopus//:libopus` so engine
  code depends on a local label, not an external.

Cold build: ~25 s on darwin-arm64. Warm: incremental.

## Rationale

### Why C (engine) over A' (cgo in gateway)

1. **Domain ownership.** The gateway is a BFF: WebRTC termination,
   JWT, session registry, fan-out. It does not and should not own
   audio processing. Once it knows "this is an Opus frame", it
   has fulfilled its layer's contract. Putting decode in the
   gateway is *responsibility bleed*. The engine is the audio
   processor. Codec work belongs there alongside whisper.cpp,
   the resampler (also libopus), and any future DSP.

2. **One-FFI-boundary rule.** Option A' pays the cgo cost for
   every 20 ms of audio (50 calls/sec/session). Option C crosses
   zero cgo boundaries on the hot path — the engine is already
   C++, libopus is already C, the call is a direct C function
   call. Per ADR-0018 Rationale #1, whisper.cpp forces the C++
   compiler to exist regardless; adding libopus next to it is
   the already-paid-for move.

3. **Wire efficiency.** Opus frames are 40–400 bytes / 20 ms.
   Raw 16 kHz mono s16 PCM is 640 bytes / 20 ms. Forwarding the
   Opus bytes verbatim is **1.6–16× smaller on the wire** than
   the decoded PCM we were sending before. Cloud-mode cross-AZ
   traffic is where this shows up on the bill.

4. **Engine can leverage Opus-native features later.** Per-frame
   FEC bits, PLC quality tuning, adaptive bitrate feedback loops
   — all need the encoder state that `libopus` exposes and
   `pion/opus` does not. Keeping decode on the engine side means
   we don't have to relitigate this boundary when those features
   come up.

### Why not A (keep pion/opus)

Pion's Opus port is a volunteer-driven subset; "just wait for
upstream" is not a viable plan when the blocker is *every
browser's default WebRTC Opus config*. Fixing this upstream
means either contributing a meaningful chunk of decoder
coverage or forking — both cost more than the C-option
execution.

### Why not A' (cgo libopus in Go)

Fair argument at product scale (ADR-0018 Recommendation B for a
product team doing horizontal scale-out). But for this repo's
portfolio optimization function, it adds a cgo boundary whose
only justification would be "we didn't want to touch the engine."
That's inertia, not architecture. The engine is where this code
belongs.

### Why not move ALL audio processing (VAD, etc.) to the gateway instead

The mirror-image framing — "centralize all audio in the gateway so
the engine is pure inference" — was briefly considered and rejected
for the same domain-boundary reason, just in the other direction.
Whisper *is* audio processing; you can't cleanly extract "just the
inference" from a whisper.cpp pipeline. The gateway's discipline is
"don't touch the samples"; the engine's discipline is "own the
samples end-to-end."

## Consequences

### Positive

- **Live WebRTC from phones works.** pion/opus's mode-3 rejection
  stops being a blocker. Real browsers with real Opus modes
  (Hybrid, CELT, SILK) all decode correctly.
- **Wire bytes drop 1.6–16×** on the gateway→engine hop (see
  Rationale #3).
- **Gateway Go code shrinks.** Opus decoder + its error handling
  + its test fixtures + its dep (~900 LOC including tests) all go
  away. The gateway becomes more obviously "a BFF."
- **Engine grows the right thing.** A small, focused `OpusDecoder`
  class lives next to whisper_engine.h / .cc, which is exactly
  where a reader looking for "where does audio come in?" expects
  to find it.
- **ADR-0018 language-choice thesis gets a concrete example.** The
  C++ engine is the natural home for libopus because it's already
  the natural home for whisper.cpp. No cgo shim, no Go developer
  needing to learn libopus's C API.

### Negative / costs

- **Build tree grows by libopus.** ~25 s cold build addition.
  Amortized by Bazel cache. Static link keeps the runtime
  dependency surface unchanged.
- **Proto wire-format evolution.** Adding `OpusChunk` variant is
  backwards-compatible, but any existing external caller hard-
  coding `IngestMessage_Pcm` needs to handle the new variant on
  receive (Gateway is the only such caller today — no impact).
- **Engine per-session state grows.** One `OpusDecoder` instance
  per live meeting. libopus's decoder state is ~30 KB. At the
  ResourceBudget (ADR-0010) scale, negligible.
- **Test coverage shift.** Gateway's Opus-path unit tests shrink
  to "are we forwarding the bytes?". Engine gains coverage on
  "are we decoding libopus correctly?". This is the right
  reshuffling — tests should live where the logic does — but the
  total test count changes.

## Alternatives Considered

### A. Keep `pion/opus`, work around mode-3

- **Pros**: No engine changes. No proto evolution.
- **Cons**: No workaround exists — mode 3 is what browsers
  actually send. The error recurs on every real WebRTC input.
  Rejected as non-viable.

### A'. Switch gateway to cgo-wrapped libopus

- **Pros**: Minimal proto diff. Gateway stays self-contained.
- **Cons**: cgo boundary on the audio hot path (50 calls / sec /
  session). Cross-compile and hermetic-build complexity migrates
  from the engine side into the gateway side — against ADR-0018
  Rationale #1 (whisper already forces C++; adding a second
  C dep to Go is the wrong place to spend the budget).
  Domain-boundary argument above also applies. Rejected.

### B. Do the decode in a third process dedicated to codec work

- **Pros**: Clean separation of concerns in the extreme.
- **Cons**: Three-tier audio pipeline (gateway → codec →
  inference) for a task that is 500 lines of code and has
  no scaling argument in its favor. Over-engineered. Rejected.

### D. Drop WebRTC altogether, use WebSocket + client-side PCM

- **Pros**: No codec on the server path at all.
- **Cons**: Browsers don't expose mic streams as PCM directly
  without cost (AudioWorklet + re-encoding), bandwidth 10–20×
  higher, loses echo-cancellation. Rejected on product-UX
  grounds.

## Implementation checklist

**Proto + build infra (done today, 2026-04-14):**

- [x] `proto/aegis/v1/aegis.proto`: add `OpusChunk` message +
      `IngestMessage.opus` variant with full comment on privacy
      posture, referenced to ADR-0005 R3.
- [x] Regenerate Go + TS proto bindings
      (`gateway_go/gen/go/aegis/v1/aegis.pb.go`,
      `frontend_web/src/gen/proto/aegis/v1/aegis_pb.ts`, …).
- [x] `MODULE.bazel`: add `libopus` http_archive pinned to 1.5.2.
- [x] `engine_cpp/third_party/libopus/libopus.BUILD`: cmake rule.
- [x] `engine_cpp/third_party/libopus/BUILD.bazel`: local alias.
- [x] `engine_cpp/src/audio/opus_decoder.h`: class interface.
- [x] Cold-build libopus clean via `bazelisk build @libopus//:libopus`
      (confirmed 25 s build, `libopus.a` ~400 KB static).

**Engine wiring (tomorrow):**

- [ ] `engine_cpp/src/audio/opus_decoder.cc`: libopus decoder
      implementation. Call `opus_decoder_create(16000, 1, …)` in
      the factory; call `opus_decode_float(…)` in Decode; map
      negative return codes to `absl::InvalidArgumentError`.
- [ ] `engine_cpp/src/audio/BUILD.bazel`: add `opus_decoder`
      cc_library with `//engine_cpp/third_party/libopus:libopus`
      and `absl/status:statusor` deps.
- [ ] `engine_cpp/src/grpc/aegis_engine_service.cc`: handle
      `payload_case() == IngestMessage::kOpus`. Per-session state
      owns one `OpusDecoder` alongside the existing
      `WhisperEngine`. Feed decoded floats directly to
      `whisper_full`.
- [ ] Unit test on `opus_decoder_test.cc`: decode a known-good
      Opus frame (generate with libopus's encoder at fixture-gen
      time), assert sample count + non-zero output. Small fixture
      checked in under `engine_cpp/testdata/opus/`.

**Gateway cleanup (tomorrow):**

- [ ] `gateway_go/internal/pipeline/pipeline.go`: remove pion/opus
      decoder; RTP loop emits `OpusChunk` instead of `PcmChunk`.
- [ ] `gateway_go/internal/pipeline/pipeline_test.go`: bufconn
      stub expects `OpusChunk`; drop the PCM-golden fixture.
- [ ] `gateway_go/go.mod` + `MODULE.bazel`: drop `github.com/pion/opus`.
- [ ] Live WebRTC from phone → engine → transcript end-to-end.

**Docs + portfolio (tomorrow):**

- [ ] `README.md` ADR index: row for 0016.
- [ ] `ARCHITECTURE.md`: update Phase 2 audio path section to
      show Opus pass-through + engine-side decode.
- [ ] `ROADMAP.md`: strike "pion/opus mode-3 rejection" from
      Phase 2 Known Gaps.
- [ ] `docs/incidents.md`: incident-09 entry for the
      pion/opus mode-3 discovery → domain-boundary refactor.
      Severity S3 (cost a feature-level refactor but no user-
      visible outage on a production system that doesn't exist
      yet). Lessons: "test on real browsers before trusting a
      pure-language library for a codec-heavy path" + "refactor-
      at-domain-boundary costs less than refactor-at-code-seam
      even when the latter looks smaller in diff".
