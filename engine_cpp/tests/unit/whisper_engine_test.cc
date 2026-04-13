// engine_cpp/tests/unit/whisper_engine_test.cc
//
// Session 4b: error-path unit tests for WhisperEngine. No model file
// is loaded here — real-inference verification is Session 4c (needs
// the ~75 MB ggml-tiny.en.bin in /models/).

#include "engine_cpp/src/inference/whisper_engine.h"

#include <string>

#include "absl/status/status.h"
#include "gtest/gtest.h"

namespace aegis::inference {
namespace {

TEST(WhisperEngineTest, CreateRejectsEmptyPath) {
  const auto result = WhisperEngine::Create("");
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kNotFound);
}

TEST(WhisperEngineTest, CreateRejectsNonExistentPath) {
  const auto result =
      WhisperEngine::Create("/definitely/does/not/exist/ggml.bin");
  ASSERT_FALSE(result.ok());
  EXPECT_EQ(result.status().code(), absl::StatusCode::kNotFound);
  EXPECT_TRUE(result.status().message().find("not found") != std::string::npos);
}

TEST(WhisperSystemInfoTest, ReturnsNonEmptyFeatureString) {
  // Proves the whisper.cpp static lib is linked and its build-time
  // feature macros got resolved. On Apple Silicon this will include
  // "NEON = 1 | ACCELERATE = 1" etc (see Session 4a README banner).
  const std::string info = WhisperSystemInfo();
  EXPECT_FALSE(info.empty());
  EXPECT_NE(info.find("CPU"), std::string::npos);
}

} // namespace
} // namespace aegis::inference
