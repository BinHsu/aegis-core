// engine_cpp/tests/integration/whisper_transcribe_test.cc
//
// Session 4c end-to-end verification: load a real whisper model,
// read a real WAV file, and confirm the transcription contains
// known content. This is the first test that exercises the full
// Session 4a+4b pipeline from bytes to words.
//
// Model: ggml-tiny.en.bin (~75 MB). Not committed to git (see
// models/.gitignore). Download via:
//   ./tools/scripts/download_models.sh --all
//
// Audio: samples/jfk.wav, bundled in the @whisper_cpp tarball —
// exposed via the :samples filegroup. JFK's inaugural address,
// 11 seconds, mono 16 kHz 16-bit PCM.
//
// Model absence is handled with GTEST_SKIP() so CI (which doesn't
// download the 75 MB file) stays green. The test runs locally once
// the developer has fetched the model.

#include <cstdlib>
#include <fstream>
#include <string>

#include "absl/strings/ascii.h"
#include "engine_cpp/src/audio/wav_reader.h"
#include "engine_cpp/src/inference/whisper_engine.h"
#include "gtest/gtest.h"

namespace aegis::inference {
namespace {

bool FileExists(const std::string &path) { return std::ifstream(path).good(); }

// Resolve the model path. Priority:
//   1. AEGIS_MODEL_DIR env (explicit override — useful for CI caches)
//   2. Bazel runfiles — if runfiles ever ships the model, pull it.
//   3. <REPO_ROOT>/models/ggml-tiny.en.bin (developer workflow).
std::string ResolveModelPath() {
  if (const char *env = std::getenv("AEGIS_MODEL_DIR"); env != nullptr) {
    return std::string(env) + "/ggml-tiny.en.bin";
  }
  if (const char *env = std::getenv("TEST_SRCDIR"); env != nullptr) {
    // Bazel runfiles root — tests execute here when invoked via `bazel test`.
    // Walk up to find a models/ sibling.
    const std::string candidate =
        std::string(env) + "/../../../../models/ggml-tiny.en.bin";
    if (FileExists(candidate))
      return candidate;
  }
  // Fallback for developers running the test binary directly.
  return "models/ggml-tiny.en.bin";
}

std::string ResolveWavPath() {
  // The :samples filegroup drops jfk.wav into the runfiles tree. Bazel's
  // external repo directory naming depends on version — 7.x uses
  // tilde-separated names (_main~_repo_rules~whisper_cpp), 8.x uses
  // plus-separated. Try both.
  if (const char *env = std::getenv("TEST_SRCDIR"); env != nullptr) {
    const std::string candidates[] = {
        std::string(env) + "/_main~_repo_rules~whisper_cpp/samples/jfk.wav",
        std::string(env) + "/+_repo_rules+whisper_cpp/samples/jfk.wav",
        std::string(env) +
            "/_main/external/_main~_repo_rules~whisper_cpp/samples/jfk.wav",
    };
    for (const auto &p : candidates) {
      if (FileExists(p))
        return p;
    }
  }
  // Fallback: the jfk.wav might be copied into test/ during local dev.
  return "test/golden_audio/jfk.wav";
}

TEST(WhisperTranscribeTest, JfkAskNot) {
  const std::string model_path = ResolveModelPath();
  if (!FileExists(model_path)) {
    GTEST_SKIP() << "model not present at " << model_path
                 << ". Run ./tools/scripts/download_models.sh --all "
                 << "or set AEGIS_MODEL_DIR to skip the download";
  }

  const std::string wav_path = ResolveWavPath();
  if (!FileExists(wav_path)) {
    GTEST_SKIP() << "audio fixture not present at " << wav_path
                 << ". Check @whisper_cpp//:samples is in test data";
  }

  // Stage 1 — decode WAV → float samples.
  const auto samples = audio::ReadWav16kMono(wav_path);
  ASSERT_TRUE(samples.ok()) << samples.status();
  EXPECT_GT(samples->size(), 0u);

  // Stage 2 — load whisper model.
  const auto engine = WhisperEngine::Create(model_path);
  ASSERT_TRUE(engine.ok()) << engine.status();

  // Stage 3 — transcribe.
  const auto text = (*engine)->Transcribe(*samples);
  ASSERT_TRUE(text.ok()) << text.status();

  // Famous opening of JFK's 1961 inaugural — matches what ggml-tiny.en
  // produces with high reliability. We lowercase + strip punctuation so
  // the assertion is robust to minor tokenizer quirks.
  std::string lower = absl::AsciiStrToLower(*text);
  // Whisper emits leading space and sometimes punctuation variants.
  EXPECT_NE(lower.find("ask not"), std::string::npos)
      << "transcription did not contain 'ask not'; got: " << *text;
  EXPECT_NE(lower.find("your country"), std::string::npos)
      << "transcription did not contain 'your country'; got: " << *text;
}

} // namespace
} // namespace aegis::inference
