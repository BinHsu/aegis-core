// engine_cpp/src/audio/opus_decoder.h
//
// Thin C++ wrapper around libopus's C decoder API. Per ADR-0016,
// Opus decoding lives on the engine side — the Gateway forwards
// RTP Opus payloads verbatim as `OpusChunk` proto messages, and
// this class decodes them into the float PCM format whisper_full
// consumes directly (no intermediate int16 step, no Go-side cgo
// crossings).
//
// Lifetime and threading:
//   - One `OpusDecoder` instance owns one `::OpusDecoder*` from
//     libopus. The C handle carries PLC / PLC-bandwidth state across
//     calls — mis-sequencing Decode() invocations from multiple
//     threads would silently corrupt that state. Callers MUST treat
//     each instance as single-thread or provide their own mutex.
//   - The engine's session registry creates one OpusDecoder per
//     session (matches the "one whisper context per session" model
//     in `inference/whisper_engine.h`) so concurrency bugs become
//     cross-session, not within a single session — easier to reason
//     about.
//
// Output format:
//   - Always 16 kHz mono float32 PCM in [-1.0, 1.0]. That matches
//     whisper.cpp's native input shape, so `whisper_full` ingests
//     our vector<float> directly.
//   - libopus does the resample + downmix internally (48 kHz stereo
//     inputs, typical of WebRTC, come out at 16 kHz mono without an
//     extra pass on our side). Fixed sample rate and channel count
//     mean we do NOT accept runtime overrides — every decoded frame
//     is 16 kHz mono.

#ifndef AEGIS_ENGINE_CPP_SRC_AUDIO_OPUS_DECODER_H_
#define AEGIS_ENGINE_CPP_SRC_AUDIO_OPUS_DECODER_H_

#include <cstddef>
#include <memory>
#include <span>
#include <vector>

#include "absl/status/statusor.h"

// Forward-declare libopus's decoder struct so the header does NOT
// include opus/opus.h — keeps the libopus transitive surface out of
// downstream compilation units.
struct OpusDecoder;

namespace aegis::audio {

// Per-call max decoded samples. Opus allows up to 120 ms / frame at
// 48 kHz → 5760 samples / channel. At our 16 kHz output that caps at
// 1920 samples / channel. We allocate for the worst case once inside
// Decode and return the sliced prefix — keeps the hot path
// allocation-free after the first call.
inline constexpr int kOutputSampleRateHz = 16000;
inline constexpr int kOutputChannels = 1;
inline constexpr int kMaxSamplesPerFrame = 1920;

class OpusDecoder {
public:
  // Construct a decoder that emits 16 kHz mono float PCM regardless
  // of the source stream's rate / channel count. Returns
  // InvalidArgument if libopus refuses the (fixed) configuration —
  // possible only if libopus itself is mis-linked, so essentially an
  // assert in disguise.
  static absl::StatusOr<std::unique_ptr<OpusDecoder>> Create();

  // Non-copyable, non-movable. Owning a raw `::OpusDecoder*` with
  // non-trivial destruction semantics — copying would double-free.
  OpusDecoder(const OpusDecoder &) = delete;
  OpusDecoder &operator=(const OpusDecoder &) = delete;
  OpusDecoder(OpusDecoder &&) = delete;
  OpusDecoder &operator=(OpusDecoder &&) = delete;
  ~OpusDecoder();

  // Decode one Opus packet. Returns decoded samples as float PCM
  // ([-1.0, 1.0], 16 kHz, mono). An empty input is InvalidArgument
  // (Opus does have a convention of passing null to signal packet
  // loss for FEC, but we do not use PLC — upstream Gateway drops
  // empty payloads before forwarding).
  //
  // Thread unsafe on a single instance — see class docs.
  absl::StatusOr<std::vector<float>>
  Decode(std::span<const std::byte> opus_payload);

private:
  explicit OpusDecoder(::OpusDecoder *dec);
  ::OpusDecoder *dec_;
};

} // namespace aegis::audio

#endif // AEGIS_ENGINE_CPP_SRC_AUDIO_OPUS_DECODER_H_
