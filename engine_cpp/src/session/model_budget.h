// engine_cpp/src/session/model_budget.h
//
// ADR-0010 §Revision (2026-04-15) — process-global model weight
// accounting. Each model loader calls Register() at startup; the
// accumulated total is read once after all models have loaded to
// size the SessionBudget pool:
//
//   SessionBudget pool = pod_memory_limit - ModelBudget::TotalUsedBytes()
//
// There is no Release(). Model weights live for the process lifetime
// (whisper, bge-m3, future LLM). If the pod can't fit all models,
// the engine refuses to boot — better a clear startup error than an
// engine that accepts zero sessions.
//
// Thread safety: Register() uses std::mutex for startup-time
// serialization. TotalUsedBytes() and Breakdown() are safe to call
// from any thread after all models have loaded (the internal vector
// is immutable after startup).

#ifndef AEGIS_ENGINE_CPP_SRC_SESSION_MODEL_BUDGET_H_
#define AEGIS_ENGINE_CPP_SRC_SESSION_MODEL_BUDGET_H_

#include <cstddef>
#include <mutex>
#include <string>
#include <utility>
#include <vector>

namespace aegis::session {

class ModelBudget {
public:
  // Register a model's memory footprint. Called by each model loader
  // at startup (WhisperEngine::Create, GGMLEmbedder::Create, etc.).
  // `model_name` is the observability label
  // (e.g. "whisper-tiny.en", "bge-m3-Q4_K_M").
  static void Register(const std::string &model_name, std::size_t bytes);

  // Total model footprint across all registered models.
  static std::size_t TotalUsedBytes();

  // Per-model breakdown for observability. Each pair is
  // {model_name, bytes}. Order matches registration order.
  static std::vector<std::pair<std::string, std::size_t>> Breakdown();

  // Reset all registrations. Test-only — production code never calls
  // this. Declared here so unit tests can run multiple scenarios
  // without process-static state leaking between test cases.
  static void ResetForTesting();

private:
  static std::mutex &Mu();
  static std::vector<std::pair<std::string, std::size_t>> &Entries();
};

} // namespace aegis::session

#endif // AEGIS_ENGINE_CPP_SRC_SESSION_MODEL_BUDGET_H_
