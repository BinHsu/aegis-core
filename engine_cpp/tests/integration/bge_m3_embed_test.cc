// engine_cpp/tests/integration/bge_m3_embed_test.cc
//
// Phase 3b Slice 4 end-to-end verification: load the real bge-m3 Q4_K_M
// GGUF weights via GGMLEmbedder and prove the runtime produces a
// sensible embedding. This is the first test that exercises the full
// ADR-0020 + ADR-0021 stack (engine-owned inference, shared ggml
// runtime) with real model weights.
//
// Model: bge-m3-Q4_K_M.gguf (~438 MB, SHA-256 pinned in
// models/manifest.json). Not committed. Fetch via:
//   ./tools/scripts/download_models.sh --model bge-m3-q4km
//
// Model absence is handled with GTEST_SKIP() so CI (which does not
// download model weights) stays green. The test runs locally once the
// developer has fetched the model.
//
// What we assert:
//   1. Create() succeeds with real weights.
//   2. Dimensions() == 1024 (bge-m3 dense embedding width).
//   3. Embed(text) returns a vector of exactly that width.
//   4. The vector is L2-normalized (||v||_2 ≈ 1).
//   5. Semantic sanity: cos-sim(related pair) > cos-sim(unrelated pair).
//      Catches "weights loaded but forward pass is broken" — an all-zeros
//      or random-output bug would fail #4 or #5, but not #1–#3.

#include <cmath>
#include <cstdlib>
#include <fstream>
#include <string>
#include <vector>

#include "absl/status/statusor.h"
#include "engine_cpp/src/inference/ggml_embedder.h"
#include "gtest/gtest.h"

namespace aegis::inference {
namespace {

constexpr int kBgeM3Dims = 1024;

bool FileExists(const std::string &path) { return std::ifstream(path).good(); }

// Resolve the model path. Priority mirrors whisper_transcribe_test:
//   1. AEGIS_MODEL_DIR env (explicit override — useful for CI caches).
//   2. Bazel runfiles — if runfiles ever ships the model, pull it.
//   3. <REPO_ROOT>/models/bge-m3-Q4_K_M.gguf (developer workflow).
std::string ResolveModelPath() {
  if (const char *env = std::getenv("AEGIS_MODEL_DIR"); env != nullptr) {
    return std::string(env) + "/bge-m3-Q4_K_M.gguf";
  }
  if (const char *env = std::getenv("TEST_SRCDIR"); env != nullptr) {
    const std::string candidate =
        std::string(env) + "/../../../../models/bge-m3-Q4_K_M.gguf";
    if (FileExists(candidate))
      return candidate;
  }
  return "models/bge-m3-Q4_K_M.gguf";
}

float CosSim(const std::vector<float> &a, const std::vector<float> &b) {
  // Inputs are L2-normalized by GGMLEmbedder, so cos-sim is just dot product.
  float dot = 0.0f;
  for (size_t i = 0; i < a.size(); ++i) {
    dot += a[i] * b[i];
  }
  return dot;
}

class BgeM3EmbedTest : public ::testing::Test {
protected:
  void SetUp() override {
    const std::string model_path = ResolveModelPath();
    if (!FileExists(model_path)) {
      GTEST_SKIP() << "bge-m3 model not present at " << model_path
                   << ". Run ./tools/scripts/download_models.sh "
                      "--model bge-m3-q4km to fetch (~438 MB), or set "
                      "AEGIS_MODEL_DIR.";
    }
    auto e = GGMLEmbedder::Create(model_path);
    ASSERT_TRUE(e.ok()) << e.status();
    embedder_ = std::move(*e);
  }

  std::unique_ptr<GGMLEmbedder> embedder_;
};

TEST_F(BgeM3EmbedTest, DimensionsMatchBgeM3) {
  EXPECT_EQ(embedder_->Dimensions(), kBgeM3Dims);
}

TEST_F(BgeM3EmbedTest, EmbedEnglishProducesNormalizedVector) {
  auto v = embedder_->Embed("The quick brown fox jumps over the lazy dog.");
  ASSERT_TRUE(v.ok()) << v.status();
  ASSERT_EQ(v->size(), static_cast<size_t>(kBgeM3Dims));

  // L2 norm should be ~1.0 (GGMLEmbedder normalizes in Embed).
  float sum_sq = 0.0f;
  for (const float x : *v)
    sum_sq += x * x;
  EXPECT_NEAR(std::sqrt(sum_sq), 1.0f, 1e-3f);
}

TEST_F(BgeM3EmbedTest, EmbedTraditionalChineseProducesNormalizedVector) {
  // Multilingual coverage — bge-m3 is picked precisely for zh-TW support
  // (ADR-0019). If this fails but English passes, the tokenizer vocab is
  // likely broken or the SentencePiece model wasn't packed in the GGUF.
  auto v = embedder_->Embed("台灣的首都是台北。");
  ASSERT_TRUE(v.ok()) << v.status();
  ASSERT_EQ(v->size(), static_cast<size_t>(kBgeM3Dims));

  float sum_sq = 0.0f;
  for (const float x : *v)
    sum_sq += x * x;
  EXPECT_NEAR(std::sqrt(sum_sq), 1.0f, 1e-3f);
}

TEST_F(BgeM3EmbedTest, SemanticallyCloseTextsHaveHigherCosineSimilarity) {
  // If the forward pass is broken (all-zeros, random output, wrong
  // pooling), dimensions + norm still look fine but semantic structure
  // collapses. This test catches that: a related pair should cluster
  // measurably closer than an unrelated pair.
  auto a = embedder_->Embed("I love cats.");
  auto b = embedder_->Embed("Cats are wonderful pets.");
  auto c = embedder_->Embed("The stock market crashed today.");
  ASSERT_TRUE(a.ok()) << a.status();
  ASSERT_TRUE(b.ok()) << b.status();
  ASSERT_TRUE(c.ok()) << c.status();

  const float sim_related = CosSim(*a, *b);
  const float sim_unrelated = CosSim(*a, *c);

  // Related pair should out-score unrelated by a comfortable margin.
  // bge-m3 on these fixtures typically gives 0.75+ vs 0.3-ish; 0.1 is a
  // very loose floor that still flags catastrophic weight corruption
  // without being fragile on quantization noise.
  EXPECT_GT(sim_related, sim_unrelated + 0.1f)
      << "related cos-sim=" << sim_related
      << " unrelated cos-sim=" << sim_unrelated;
}

} // namespace
} // namespace aegis::inference
