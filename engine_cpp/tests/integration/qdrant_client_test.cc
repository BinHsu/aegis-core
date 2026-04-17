// engine_cpp/tests/integration/qdrant_client_test.cc
//
// End-to-end smoke for QdrantClient against a real Qdrant instance.
// Exercises the three client methods (CreateCollection, UpsertPoints,
// Search) in a single round-trip.
//
// Gated on the QDRANT_URL env var — GTEST_SKIP when unset so CI stays
// green without standing up Qdrant. Local developers follow the
// runbook at docs/runbooks/qdrant-local-setup.md to get a Qdrant
// server running, then invoke:
//
//   QDRANT_URL=localhost:6334 \
//   ./tools/bazelisk/bazelisk test \
//     //engine_cpp/tests/integration:qdrant_client_test \
//     --test_env=QDRANT_URL \
//     --test_output=all
//
// Optional: QDRANT_API_KEY for cloud targets.
//
// Each run uses a test-unique collection name so concurrent runs
// against the same Qdrant instance do not collide.

#include "engine_cpp/src/vectordb/qdrant_client.h"

#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <random>
#include <string>
#include <vector>

#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "gtest/gtest.h"

namespace aegis::vectordb {
namespace {

constexpr int kTestVectorDim = 4;

// Generate a per-run collection name so concurrent runs do not collide.
std::string UniqueCollectionName() {
  const auto now_ns = std::chrono::duration_cast<std::chrono::nanoseconds>(
                          std::chrono::steady_clock::now().time_since_epoch())
                          .count();
  std::mt19937_64 rng(static_cast<uint64_t>(now_ns));
  return absl::StrCat("aegis_test_", absl::Hex(rng()), "_collection");
}

class QdrantIntegrationTest : public ::testing::Test {
protected:
  void SetUp() override {
    if (std::getenv("QDRANT_URL") == nullptr) {
      GTEST_SKIP() << "QDRANT_URL not set; skipping integration test. "
                      "Run Qdrant locally per docs/runbooks/"
                      "qdrant-local-setup.md and re-run with "
                      "--test_env=QDRANT_URL.";
    }
    auto cfg = QdrantClient::ConfigFromEnv();
    ASSERT_TRUE(cfg.ok()) << cfg.status();
    auto client = QdrantClient::Create(*cfg);
    ASSERT_TRUE(client.ok()) << client.status();
    client_ = std::move(*client);
    collection_ = UniqueCollectionName();
  }

  std::unique_ptr<QdrantClient> client_;
  std::string collection_;
};

TEST_F(QdrantIntegrationTest, CreateUpsertSearchRoundTrip) {
  // Stage 1 — create collection.
  ASSERT_TRUE(client_
                  ->CreateCollection(collection_, kTestVectorDim,
                                     DistanceMetric::kCosine)
                  .ok());

  // Stage 2 — upsert four deterministic points. Each vector's first
  // component is a unit axis; the search below probes the +x axis and
  // expects the first point (id "p0") to win with score ~1.0.
  std::vector<Point> points;
  const std::vector<std::vector<float>> vectors = {
      {1.0f, 0.0f, 0.0f, 0.0f}, // +x
      {0.0f, 1.0f, 0.0f, 0.0f}, // +y
      {0.0f, 0.0f, 1.0f, 0.0f}, // +z
      {0.0f, 0.0f, 0.0f, 1.0f}, // +w
  };
  for (int i = 0; i < 4; ++i) {
    Point p;
    p.id = absl::StrCat("00000000-0000-0000-0000-00000000000", i);
    p.vector = vectors[i];
    p.payload = {{"label", absl::StrCat("axis_", i)}};
    points.push_back(std::move(p));
  }
  ASSERT_TRUE(client_->UpsertPoints(collection_, points).ok());

  // Stage 3 — search for the +x axis; expect the first point wins.
  auto results =
      client_->Search(collection_, std::vector<float>{1.0f, 0.0f, 0.0f, 0.0f},
                      /*top_k=*/3);
  ASSERT_TRUE(results.ok()) << results.status();
  ASSERT_FALSE(results->empty());

  // Top result is the +x point, with cosine-similarity ≈ 1.
  EXPECT_EQ(results->at(0).id, points[0].id);
  EXPECT_NEAR(results->at(0).score, 1.0f, 1e-3f);
  EXPECT_EQ(results->at(0).payload["label"], "axis_0");

  // Remaining results are orthogonal axes, score ≈ 0.
  for (size_t i = 1; i < results->size(); ++i) {
    EXPECT_NEAR(results->at(i).score, 0.0f, 1e-3f);
  }
}

TEST_F(QdrantIntegrationTest, CreateCollectionIsIdempotent) {
  ASSERT_TRUE(client_
                  ->CreateCollection(collection_, kTestVectorDim,
                                     DistanceMetric::kCosine)
                  .ok());
  // Second create with matching params: OK via the CollectionExists
  // fast-path in QdrantClient::CreateCollection.
  EXPECT_TRUE(client_
                  ->CreateCollection(collection_, kTestVectorDim,
                                     DistanceMetric::kCosine)
                  .ok());
}

TEST_F(QdrantIntegrationTest, UpsertRejectsEmptyPointId) {
  ASSERT_TRUE(client_
                  ->CreateCollection(collection_, kTestVectorDim,
                                     DistanceMetric::kCosine)
                  .ok());
  Point p;
  p.id = "";
  p.vector = {1.0f, 0.0f, 0.0f, 0.0f};
  auto status = client_->UpsertPoints(collection_, std::vector<Point>{p});
  ASSERT_FALSE(status.ok());
  EXPECT_EQ(status.code(), absl::StatusCode::kInvalidArgument);
}

TEST_F(QdrantIntegrationTest, UpsertEmptySpanIsNoOp) {
  ASSERT_TRUE(client_
                  ->CreateCollection(collection_, kTestVectorDim,
                                     DistanceMetric::kCosine)
                  .ok());
  EXPECT_TRUE(client_->UpsertPoints(collection_, {}).ok());
}

} // namespace
} // namespace aegis::vectordb
