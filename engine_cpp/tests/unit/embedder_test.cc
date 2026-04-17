// engine_cpp/tests/unit/embedder_test.cc
//
// Unit tests for the abstract Embedder base class. Exercises:
//   - the default EmbedBatch implementation (loops over Embed,
//     preserves order, propagates errors)
//   - Dimensions() + ModelTag() stability
//
// Real embedder impls (GGMLEmbedder, RemoteEmbedder) land in later
// Phase 3b slices and get their own test files.

#include "engine_cpp/src/inference/embedder.h"

#include <string_view>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/types/span.h"
#include "gtest/gtest.h"

namespace aegis::inference {
namespace {

// Minimal Embedder stub. Returns the length of each input as a
// single-element vector — just enough to verify that EmbedBatch
// invokes Embed once per input in order.
class LengthEmbedder : public Embedder {
public:
  absl::StatusOr<std::vector<float>> Embed(std::string_view text) override {
    if (text.empty()) {
      return absl::InvalidArgumentError("LengthEmbedder: empty text");
    }
    return std::vector<float>{static_cast<float>(text.size())};
  }
  int Dimensions() const override { return 1; }
  std::string_view ModelTag() const override { return "length-stub/v1"; }
};

TEST(EmbedderTest, EmbedBatchDelegatesPerInputAndPreservesOrder) {
  LengthEmbedder e;
  std::vector<std::string_view> inputs = {"a", "bb", "ccc"};
  auto out = e.EmbedBatch(absl::MakeConstSpan(inputs));
  ASSERT_TRUE(out.ok()) << out.status();
  ASSERT_EQ(out->size(), 3u);
  EXPECT_FLOAT_EQ(out->at(0)[0], 1.0f);
  EXPECT_FLOAT_EQ(out->at(1)[0], 2.0f);
  EXPECT_FLOAT_EQ(out->at(2)[0], 3.0f);
}

TEST(EmbedderTest, EmbedBatchShortCircuitsOnFirstError) {
  LengthEmbedder e;
  // Second input is empty — LengthEmbedder returns InvalidArgument.
  std::vector<std::string_view> inputs = {"a", "", "ccc"};
  auto out = e.EmbedBatch(absl::MakeConstSpan(inputs));
  ASSERT_FALSE(out.ok());
  EXPECT_EQ(out.status().code(), absl::StatusCode::kInvalidArgument);
}

TEST(EmbedderTest, EmbedBatchOnEmptyInputReturnsEmptyOutput) {
  LengthEmbedder e;
  std::vector<std::string_view> inputs;
  auto out = e.EmbedBatch(absl::MakeConstSpan(inputs));
  ASSERT_TRUE(out.ok());
  EXPECT_TRUE(out->empty());
}

TEST(EmbedderTest, DimensionsAndModelTagAreStable) {
  LengthEmbedder e;
  EXPECT_EQ(e.Dimensions(), 1);
  EXPECT_EQ(e.Dimensions(), 1);
  EXPECT_EQ(e.ModelTag(), "length-stub/v1");
}

} // namespace
} // namespace aegis::inference
