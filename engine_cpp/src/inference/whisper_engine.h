// engine_cpp/src/inference/whisper_engine.h
//
// Thin C++20 wrapper around whisper.cpp. Session 4a intentionally
// exposes only a minimal surface (system info query) to validate the
// build integration. Session 4b adds whisper_context lifecycle,
// Session 4c wires transcription through IngestMessage streams.

#ifndef AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_
#define AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_

#include <string>

namespace aegis::inference {

// Returns whisper.cpp's compile-time system/feature string (AVX, Metal,
// CUDA, etc.). Calling this at process start proves the whisper+ggml
// static libraries are linked correctly and their runtime data sections
// are reachable. Never blocks, never allocates on failure.
std::string WhisperSystemInfo();

} // namespace aegis::inference

#endif // AEGIS_ENGINE_CPP_SRC_INFERENCE_WHISPER_ENGINE_H_
