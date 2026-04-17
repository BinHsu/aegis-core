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
    const std::string &text,
    ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                               aegis::v1::IngestMessage> *stream) {
  if (text.empty()) {
    return absl::OkStatus();
  }
  aegis::v1::EgressMessage egress;
  auto *seg = egress.mutable_transcript();
  seg->set_segment_id(1);
  seg->set_speaker_label("Speaker_0"); // single-stream Phase 1
  seg->set_start_ms(0);
  seg->set_end_ms(0);
  seg->set_text(text);
  seg->set_language("en");
  seg->set_is_final(true);
  seg->set_is_question(false);
  seg->set_confidence(0.0f);
  if (!stream->Write(egress)) {
    return absl::AbortedError(
        "Session: client closed stream before final write");
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
  State state = State::Active;
  std::vector<float> samples;
  // Pre-reserve ~30 seconds of 16 kHz mono to avoid mid-loop rallocs.
  samples.reserve(30 * 16000);

  // OpusDecoder is lazy-init on the first OpusChunk — sessions that
  // stream only PcmChunk (WAV fixture replay, push-to-talk) never pay
  // the libopus setup cost. Per ADR-0016 one instance per session;
  // libopus carries PLC state across calls.
  std::unique_ptr<aegis::audio::OpusDecoder> opus_decoder;

  while (stream->Read(&msg)) {
    if (msg.has_session_start()) {
      budget_->Release(reserve_bytes);
      return absl::InvalidArgumentError(
          "Session: SessionStart received mid-stream");
    }

    if (msg.has_pcm()) {
      if (state == State::Active) {
        AppendInt16LEAsFloat(msg.pcm().pcm(), &samples);
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

  // Stage 5 — flush: transcribe accumulated PCM and emit segments.
  if (!samples.empty()) {
    auto text_or = engine->Transcribe(samples);
    if (!text_or.ok()) {
      budget_->Release(reserve_bytes);
      return text_or.status();
    }
    if (auto s = EmitTranscriptSegments(*text_or, stream); !s.ok()) {
      budget_->Release(reserve_bytes);
      return s;
    }
  }

  budget_->Release(reserve_bytes);
  return absl::OkStatus();
}

} // namespace aegis::session
