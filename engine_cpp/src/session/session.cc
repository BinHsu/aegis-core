// engine_cpp/src/session/session.cc

#include "engine_cpp/src/session/session.h"

#include <cstddef>
#include <cstdint>
#include <cstring>
#include <memory>
#include <span>
#include <utility>
#include <vector>

#include "absl/status/status.h"
#include "absl/strings/str_cat.h"
#include "engine_cpp/src/audio/opus_decoder.h"
#include "engine_cpp/src/inference/whisper_engine.h"
#include "engine_cpp/src/session/session_budget.h"
#include "proto/aegis/v1/aegis.pb.h"

namespace aegis::session {

namespace {

// Default per-session reservation per ADR-0010 Sub-decision 2
// (conservative 200 MB for whisper+ggml+ring buffer at tiny.en scale).
// SessionStart.estimated_bytes overrides if set.
constexpr std::size_t kDefaultReservationBytes = 200ULL * 1024 * 1024;

// Live-transcribe window size (MVP).
//
// 3 seconds @ 16 kHz mono = 48000 samples. Trade-off summary:
//   - Smaller window → lower latency to first segment, worse whisper
//     accuracy (whisper's encoder was trained for ~30 s windows).
//   - Larger window → better accuracy, higher first-segment latency,
//     higher per-session peak memory.
// 3 s is the cheapest "live" cadence that still produces usable
// transcription on tiny.en. No overlap is kept for MVP — words may
// cut at window boundaries; VAD-based boundary selection is Phase 2+.
// Final-flush at stream end (below) emits the remaining < window
// sub-window so no audio is lost on EndMeeting.
constexpr std::size_t kSampleRateHz = 16000;
constexpr std::size_t kLiveWindowSeconds = 3;
constexpr std::size_t kLiveWindowSamples = kLiveWindowSeconds * kSampleRateHz;

enum class State {
  WaitingForStart,
  Active,
  Paused,
};

// Convert a packed int16 LE PCM payload (as carried on
// IngestMessage.PcmChunk.pcm bytes) into float samples in [-1, 1].
// Uses the canonical whisper.cpp input normalization.
void AppendInt16LEAsFloat(const std::string &int16_le_bytes,
                          std::vector<float> *out) {
  const std::size_t n_samples = int16_le_bytes.size() / sizeof(int16_t);
  out->reserve(out->size() + n_samples);
  for (std::size_t i = 0; i < n_samples; ++i) {
    int16_t s = 0;
    std::memcpy(&s, int16_le_bytes.data() + i * sizeof(int16_t),
                sizeof(int16_t));
    out->push_back(static_cast<float>(s) / 32768.0f);
  }
}

absl::Status EmitTranscriptSegments(
    const std::string &text, std::uint64_t segment_id, std::int64_t start_ms,
    std::int64_t end_ms,
    ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                               aegis::v1::IngestMessage> *stream) {
  if (text.empty()) {
    return absl::OkStatus();
  }
  aegis::v1::EgressMessage egress;
  auto *seg = egress.mutable_transcript();
  seg->set_segment_id(segment_id);
  seg->set_speaker_label("Speaker_0"); // single-stream Phase 1
  seg->set_start_ms(start_ms);
  seg->set_end_ms(end_ms);
  seg->set_text(text);
  seg->set_language("en");
  seg->set_is_final(true);
  seg->set_is_question(false);
  seg->set_confidence(0.0f);
  if (!stream->Write(egress)) {
    return absl::AbortedError(
        "Session: client closed stream before transcript write");
  }
  return absl::OkStatus();
}

} // namespace

Session::Session(SessionBudget *budget, const std::string &model_path) noexcept
    : budget_(budget), model_path_(model_path) {}

absl::Status
Session::Run(::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                                        aegis::v1::IngestMessage> *stream) {
  // Stage 1 — expect SessionStart as the first message.
  aegis::v1::IngestMessage msg;
  if (!stream->Read(&msg)) {
    return absl::AbortedError("Session: client closed before SessionStart");
  }
  if (!msg.has_session_start()) {
    return absl::InvalidArgumentError(
        "Session: first message must be SessionStart");
  }
  const auto &start = msg.session_start();

  // Stage 2 — reserve budget. RAII via scope guard so every exit path
  // (including early errors below) releases correctly.
  const std::size_t reserve_bytes =
      start.estimated_bytes() > 0
          ? static_cast<std::size_t>(start.estimated_bytes())
          : kDefaultReservationBytes;
  if (auto s = budget_->Reserve(reserve_bytes); !s.ok()) {
    return s; // RESOURCE_EXHAUSTED bubbles up
  }
  // From here on, every return path MUST call budget_->Release(reserve_bytes).
  // This is tracked by hand rather than RAII because the per-call value
  // depends on SessionStart.estimated_bytes; an RAII wrapper would need
  // to be constructed after Reserve anyway. See ADR-0010 Sub-decision 2.

  // Stage 3 — load a per-session whisper context. On Phase 1 we pay the
  // ~100-300ms load on every session (ADR-0010 1-session-1-thread model).
  // The Phase 2+ MPSC+worker pool upgrade path would introduce a shared
  // pool here.
  auto engine_or = aegis::inference::WhisperEngine::Create(model_path_);
  if (!engine_or.ok()) {
    budget_->Release(reserve_bytes);
    return engine_or.status();
  }
  auto engine = std::move(*engine_or);

  // Stage 4 — state machine over IngestMessage stream.
  //
  // Live windowing model (MVP):
  //   - samples accumulates incoming PCM.
  //   - Whenever samples.size() crosses kLiveWindowSamples, flush_window()
  //     runs Transcribe + Emit + clears the buffer (non-overlapping).
  //   - After the stream ends (END_STREAM or client close), a final
  //     force-flush emits whatever sub-window remains.
  // Each emitted segment carries start_ms/end_ms derived from the
  // cumulative sample offset, so viewers can align transcript text to
  // real wall-clock positions. segment_id is a session-local monotonic
  // counter.
  State state = State::Active;
  std::vector<float> samples;
  samples.reserve(kLiveWindowSamples + kSampleRateHz);

  std::uint64_t next_segment_id = 1;
  std::uint64_t samples_emitted = 0; // total samples already covered by
                                     // an emitted segment (for timestamps)

  // OpusDecoder is lazy-init on the first OpusChunk — sessions that
  // stream only PcmChunk (WAV fixture replay, push-to-talk) never pay
  // the libopus setup cost. Per ADR-0016 one instance per session;
  // libopus carries PLC state across calls.
  std::unique_ptr<aegis::audio::OpusDecoder> opus_decoder;

  // flush_window — if samples has accumulated at least one full window
  // (or force=true for end-of-stream flush), Transcribe it, emit a
  // segment (with start_ms/end_ms computed from samples_emitted), then
  // clear samples. Transcribe errors are swallowed so a single bad
  // window does not tear the session down — this mirrors the ADR-0016
  // "log-and-drop" stance on per-frame Opus decode errors.
  auto flush_window = [&](bool force) -> absl::Status {
    if (samples.empty()) {
      return absl::OkStatus();
    }
    if (!force && samples.size() < kLiveWindowSamples) {
      return absl::OkStatus();
    }
    const std::size_t window_n = samples.size();
    auto text_or = engine->Transcribe(samples);
    samples.clear();
    const std::int64_t start_ms =
        static_cast<std::int64_t>((samples_emitted * 1000) / kSampleRateHz);
    samples_emitted += window_n;
    const std::int64_t end_ms =
        static_cast<std::int64_t>((samples_emitted * 1000) / kSampleRateHz);
    if (!text_or.ok()) {
      // Log-and-drop this window; continue the session.
      return absl::OkStatus();
    }
    if (text_or->empty()) {
      // Whisper produced silence — do not emit an empty segment. The
      // segment_id is NOT incremented; it only counts emitted segments.
      return absl::OkStatus();
    }
    if (auto s = EmitTranscriptSegments(*text_or, next_segment_id++, start_ms,
                                        end_ms, stream);
        !s.ok()) {
      return s;
    }
    return absl::OkStatus();
  };

  while (stream->Read(&msg)) {
    if (msg.has_session_start()) {
      budget_->Release(reserve_bytes);
      return absl::InvalidArgumentError(
          "Session: SessionStart received mid-stream");
    }

    if (msg.has_pcm()) {
      if (state == State::Active) {
        AppendInt16LEAsFloat(msg.pcm().pcm(), &samples);
        if (auto s = flush_window(/*force=*/false); !s.ok()) {
          budget_->Release(reserve_bytes);
          return s;
        }
      }
      // In Paused state we deliberately drop PCM per ADR-0006.
      continue;
    }

    if (msg.has_opus()) {
      if (state == State::Active) {
        if (opus_decoder == nullptr) {
          auto dec_or = aegis::audio::OpusDecoder::Create();
          if (!dec_or.ok()) {
            budget_->Release(reserve_bytes);
            return dec_or.status();
          }
          opus_decoder = std::move(*dec_or);
        }
        const std::string &payload = msg.opus().opus();
        auto pcm_or = opus_decoder->Decode(std::span<const std::byte>(
            reinterpret_cast<const std::byte *>(payload.data()),
            payload.size()));
        if (pcm_or.ok()) {
          samples.insert(samples.end(), pcm_or->begin(), pcm_or->end());
          if (auto s = flush_window(/*force=*/false); !s.ok()) {
            budget_->Release(reserve_bytes);
            return s;
          }
        }
        // On decode error: log-and-drop per ADR-0016. A single
        // corrupt 20 ms frame should not tear down a session.
        // Logging infra on this path lands in Phase 4; for now
        // the drop is silent.
      }
      // Paused: drop, same contract as PcmChunk.
      continue;
    }

    if (msg.has_control()) {
      const auto kind = msg.control().kind();
      if (kind == aegis::v1::CONTROL_KIND_PAUSE) {
        state = State::Paused;
      } else if (kind == aegis::v1::CONTROL_KIND_RESUME) {
        state = State::Active;
      } else if (kind == aegis::v1::CONTROL_KIND_END_STREAM) {
        break;
      }
      // UNSPECIFIED: ignore silently — it is a proto default, not a signal.
      continue;
    }
    // Any other oneof branch is a proto-schema violation by the Gateway.
    budget_->Release(reserve_bytes);
    return absl::InvalidArgumentError(
        "Session: IngestMessage with unknown payload");
  }

  // Stage 5 — final flush for the trailing sub-window that never hit
  // the live-window threshold. Ensures no audio is lost on EndMeeting.
  if (auto s = flush_window(/*force=*/true); !s.ok()) {
    budget_->Release(reserve_bytes);
    return s;
  }

  budget_->Release(reserve_bytes);
  return absl::OkStatus();
}

} // namespace aegis::session
