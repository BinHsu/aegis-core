// engine_cpp/src/inference/whisper_engine.h
//
// C++20 RAII wrapper around whisper.cpp's whisper_context. Session 4b
// exposes Create / Transcribe with absl::Status error handling per
// ADR-0010 Sub-decision 3 (exceptions banned in engine_cpp/src).
//
// Thread safety: a single WhisperEngine instance is NOT thread-safe
// for concurrent Transcribe calls (whisper.cpp's context is stateful
// across calls). Per-session instances aligned with the 1-session-
// 1-thread model of ADR-0010 Sub-decision 1. Session 5+ may introduce
// a pool if we move to the MPSC+worker upgrade path.

#ifndef AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_
#define AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_

#include <cstdint>
#include <memory>
#include <span>
#include <string>

#include "absl/status/status.h"
#include "absl/status/statusor.h"

// Forward-declare whisper's opaque context to keep whisper.h out of
// this public header. Downstream callers include only the wrapper.
struct whisper_context;

namespace aegis::inference {

// Returns whisper.cpp's compile-time system/feature string (AVX, Metal,
// CUDA, etc.). Calling this at process start proves the whisper+ggml
// static libraries are linked correctly.
std::string WhisperSystemInfo();

class WhisperEngine {
public:
  // Load a whisper.cpp model file (ggml / gguf) from `model_path` and
  // return a ready-to-use engine. Errors:
  //   NotFoundError      — path is empty or file does not exist.
  //   InternalError      — whisper_init_from_file returned null.
  static absl::StatusOr<std::unique_ptr<WhisperEngine>>
  Create(const std::string &model_path);

  ~WhisperEngine();

  // Transcribe a chunk of 16 kHz mono float PCM audio. Returns the
  // concatenated text of all segments whisper produces. Errors:
  //   InvalidArgumentError — samples is empty.
  //   InternalError        — whisper_full returned non-zero.
  absl::StatusOr<std::string> Transcribe(std::span<const float> samples);

  // Non-copyable, non-movable. Sessions own their own engine.
  WhisperEngine(const WhisperEngine &) = delete;
  WhisperEngine &operator=(const WhisperEngine &) = delete;
  WhisperEngine(WhisperEngine &&) = delete;
  WhisperEngine &operator=(WhisperEngine &&) = delete;

private:
  explicit WhisperEngine(whisper_context *ctx) noexcept;
  whisper_context *ctx_; // owned — freed in dtor via whisper_free
};

} // namespace aegis::inference

#endif // AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_
