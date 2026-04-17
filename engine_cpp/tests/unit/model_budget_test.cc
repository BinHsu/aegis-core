// engine_cpp/tests/unit/model_budget_test.cc
//
// Unit tests for ModelBudget — the process-global model weight
// accounting introduced in ADR-0010 §Revision (2026-04-15).

#include "engine_cpp/src/session/model_budget.h"

#include <cstddef>
#include <string>
#include <utility>
#include <vector>

#include "gtest/gtest.h"

namespace aegis::session {
namespace {

class ModelBudgetTest : public ::testing::Test {
protected:
  void TearDown() override { ModelBudget::ResetForTesting(); }
};

TEST_F(ModelBudgetTest, EmptyByDefault) {
  EXPECT_EQ(ModelBudget::TotalUsedBytes(), 0u);
  EXPECT_TRUE(ModelBudget::Breakdown().empty());
}

TEST_F(ModelBudgetTest, SingleModelRegistration) {
  ModelBudget::Register("whisper-tiny.en", 75 * 1024 * 1024);
  EXPECT_EQ(ModelBudget::TotalUsedBytes(), 75u * 1024 * 1024);
  auto breakdown = ModelBudget::Breakdown();
  ASSERT_EQ(breakdown.size(), 1u);
  EXPECT_EQ(breakdown[0].first, "whisper-tiny.en");
  EXPECT_EQ(breakdown[0].second, 75u * 1024 * 1024);
}

TEST_F(ModelBudgetTest, MultipleModelsAccumulate) {
  ModelBudget::Register("whisper-tiny.en", 75 * 1024 * 1024);
  ModelBudget::Register("bge-m3-Q4_K_M", 400ULL * 1024 * 1024);

  const std::size_t expected = (75ULL + 400ULL) * 1024 * 1024;
  EXPECT_EQ(ModelBudget::TotalUsedBytes(), expected);

  auto breakdown = ModelBudget::Breakdown();
  ASSERT_EQ(breakdown.size(), 2u);
  EXPECT_EQ(breakdown[0].first, "whisper-tiny.en");
  EXPECT_EQ(breakdown[1].first, "bge-m3-Q4_K_M");
}

TEST_F(ModelBudgetTest, BreakdownPreservesRegistrationOrder) {
  ModelBudget::Register("c", 300);
  ModelBudget::Register("a", 100);
  ModelBudget::Register("b", 200);

  auto breakdown = ModelBudget::Breakdown();
  ASSERT_EQ(breakdown.size(), 3u);
  EXPECT_EQ(breakdown[0].first, "c");
  EXPECT_EQ(breakdown[1].first, "a");
  EXPECT_EQ(breakdown[2].first, "b");
  EXPECT_EQ(ModelBudget::TotalUsedBytes(), 600u);
}

TEST_F(ModelBudgetTest, ResetClearsAllRegistrations) {
  ModelBudget::Register("model-a", 1000);
  ASSERT_EQ(ModelBudget::TotalUsedBytes(), 1000u);
  ModelBudget::ResetForTesting();
  EXPECT_EQ(ModelBudget::TotalUsedBytes(), 0u);
  EXPECT_TRUE(ModelBudget::Breakdown().empty());
}

} // namespace
} // namespace aegis::session
