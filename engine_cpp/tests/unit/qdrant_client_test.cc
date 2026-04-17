// engine_cpp/tests/unit/qdrant_client_test.cc
//
// Unit coverage for QdrantClient's env-parsing and config-validation
// paths — the bits that do not need a live Qdrant server to exercise.
// End-to-end behavior (CreateCollection / UpsertPoints / Search
// round-trip) is covered in the integration test, gated on the
// QDRANT_URL env var.

#include "engine_cpp/src/vectordb/qdrant_client.h"

#include <cstdlib>
#include <map>
#include <string>
#include <utility>

#include "absl/status/status.h"
#include "gtest/gtest.h"

namespace aegis::vectordb {
namespace {

class QdrantEnvTest : public ::testing::Test {
protected:
  void SetUp() override {
    // Snapshot any existing values so we restore in TearDown; tests
    // manipulate QDRANT_URL / QDRANT_API_KEY freely.
    SaveEnv("QDRANT_URL");
    SaveEnv("QDRANT_API_KEY");
  }
  void TearDown() override {
    RestoreEnv("QDRANT_URL");
    RestoreEnv("QDRANT_API_KEY");
  }

private:
  void SaveEnv(const char *name) {
    if (const char *v = std::getenv(name); v != nullptr) {
      saved_[name] = {true, v};
    } else {
      saved_[name] = {false, ""};
    }
    ::unsetenv(name);
  }
  void RestoreEnv(const char *name) {
    const auto &s = saved_[name];
    if (s.first) {
      ::setenv(name, s.second.c_str(), 1);
    } else {
      ::unsetenv(name);
    }
  }
  std::map<std::string, std::pair<bool, std::string>> saved_;
};

TEST_F(QdrantEnvTest, ConfigFromEnvFailsWhenUrlMissing) {
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_FALSE(cfg.ok());
  EXPECT_EQ(cfg.status().code(), absl::StatusCode::kInvalidArgument);
  EXPECT_NE(cfg.status().message().find("QDRANT_URL"), std::string::npos);
}

TEST_F(QdrantEnvTest, ConfigFromEnvFailsWhenUrlEmpty) {
  ::setenv("QDRANT_URL", "", 1);
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_FALSE(cfg.ok());
  EXPECT_EQ(cfg.status().code(), absl::StatusCode::kInvalidArgument);
}

TEST_F(QdrantEnvTest, ConfigFromEnvBareHostPortIsPlaintext) {
  ::setenv("QDRANT_URL", "localhost:6334", 1);
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_TRUE(cfg.ok()) << cfg.status();
  EXPECT_EQ(cfg->endpoint, "localhost:6334");
  EXPECT_FALSE(cfg->use_tls);
  EXPECT_TRUE(cfg->api_key.empty());
}

TEST_F(QdrantEnvTest, ConfigFromEnvHttpsSchemeEnablesTlsAndStrips) {
  ::setenv("QDRANT_URL", "https://xxx.aws.cloud.qdrant.io:6334", 1);
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_TRUE(cfg.ok()) << cfg.status();
  EXPECT_EQ(cfg->endpoint, "xxx.aws.cloud.qdrant.io:6334");
  EXPECT_TRUE(cfg->use_tls);
}

TEST_F(QdrantEnvTest, ConfigFromEnvHttpSchemeStripsWithoutTls) {
  ::setenv("QDRANT_URL", "http://qdrant.internal:6334", 1);
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_TRUE(cfg.ok()) << cfg.status();
  EXPECT_EQ(cfg->endpoint, "qdrant.internal:6334");
  EXPECT_FALSE(cfg->use_tls);
}

TEST_F(QdrantEnvTest, ConfigFromEnvReadsApiKey) {
  ::setenv("QDRANT_URL", "localhost:6334", 1);
  ::setenv("QDRANT_API_KEY", "qdrant-test-key", 1);
  auto cfg = QdrantClient::ConfigFromEnv();
  ASSERT_TRUE(cfg.ok()) << cfg.status();
  EXPECT_EQ(cfg->api_key, "qdrant-test-key");
}

TEST(QdrantClientCreateTest, CreateRejectsEmptyEndpoint) {
  QdrantClient::Config cfg;
  cfg.endpoint = "";
  auto client = QdrantClient::Create(cfg);
  ASSERT_FALSE(client.ok());
  EXPECT_EQ(client.status().code(), absl::StatusCode::kInvalidArgument);
}

TEST(QdrantClientCreateTest, CreateOpensChannelOnValidEndpoint) {
  // A valid-looking but unreachable endpoint still produces a usable
  // client — gRPC channels are lazy, so the error surfaces on the
  // first RPC, not at Create time. Integration coverage exercises the
  // real RPC path.
  QdrantClient::Config cfg;
  cfg.endpoint = "localhost:1";
  auto client = QdrantClient::Create(cfg);
  ASSERT_TRUE(client.ok()) << client.status();
  EXPECT_NE(*client, nullptr);
}

} // namespace
} // namespace aegis::vectordb
