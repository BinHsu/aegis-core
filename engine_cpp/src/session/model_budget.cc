// engine_cpp/src/session/model_budget.cc

#include "engine_cpp/src/session/model_budget.h"

#include <cstddef>
#include <mutex>
#include <string>
#include <utility>
#include <vector>

namespace aegis::session {

// Use function-local statics to avoid SIOF (Static Initialization
// Order Fiasco). The mutex and entries vector are constructed on
// first access, which is guaranteed to happen during startup model
// loading — well before any concurrent session threads exist.

std::mutex &ModelBudget::Mu() {
  static std::mutex mu;
  return mu;
}

std::vector<std::pair<std::string, std::size_t>> &ModelBudget::Entries() {
  static std::vector<std::pair<std::string, std::size_t>> entries;
  return entries;
}

void ModelBudget::Register(const std::string &model_name, std::size_t bytes) {
  std::lock_guard<std::mutex> lock(Mu());
  Entries().emplace_back(model_name, bytes);
}

std::size_t ModelBudget::TotalUsedBytes() {
  std::lock_guard<std::mutex> lock(Mu());
  std::size_t total = 0;
  for (const auto &[name, bytes] : Entries()) {
    total += bytes;
  }
  return total;
}

std::vector<std::pair<std::string, std::size_t>> ModelBudget::Breakdown() {
  std::lock_guard<std::mutex> lock(Mu());
  return Entries();
}

void ModelBudget::ResetForTesting() {
  std::lock_guard<std::mutex> lock(Mu());
  Entries().clear();
}

} // namespace aegis::session
