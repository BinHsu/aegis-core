// engine_cpp/src/session/session_budget.h
//
// ADR-0010 Sub-decision 2, revised 2026-04-15 — per-session memory
// budget tracking. Prevents OOM by tracking total allocated bytes
// across all active sessions and rejecting new sessions that would
// exceed the configured ceiling. Rejection surfaces as
// gRPC::StatusCode::RESOURCE_EXHAUSTED at the RPC boundary.
//
// The pool size passed to the constructor should be:
//   (pod_memory_limit - ModelBudget::TotalUsedBytes())
// computed at engine startup AFTER all models have registered.
// See ADR-0010 §Revision and model_budget.h.
//
// Thread-safe via std::atomic. Reserve() / Release() must be paired
// 1:1 — typically via RAII in a session-scoped scope guard.

#ifndef AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_BUDGET_H_
#define AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_BUDGET_H_

#include <atomic>
#include <cstddef>

#include "absl/status/status.h"

namespace aegis::session {

class SessionBudget {
public:
  // `total_bytes` is the session pool size, NOT the pod memory limit.
  // Callers compute it as (pod_limit - ModelBudget::TotalUsedBytes()).
  explicit SessionBudget(std::size_t total_bytes) noexcept;

  // Try to reserve `bytes`. Returns OK on success; ResourceExhaustedError
  // if the reservation would exceed the budget. Over-commit is forbidden.
  absl::Status Reserve(std::size_t bytes);

  // Release a prior reservation. Must be paired 1:1 with Reserve().
  // Never fails.
  void Release(std::size_t bytes) noexcept;

  // Observability — exported as Prometheus metric
  // `aegis_engine_session_bytes_used` per ADR-0010 §Revision.
  std::size_t BytesUsed() const noexcept;
  std::size_t BytesAvailable() const noexcept;
  std::size_t TotalBytes() const noexcept { return total_bytes_; }

  // Non-copyable / non-movable — SessionBudget is a process singleton.
  SessionBudget(const SessionBudget &) = delete;
  SessionBudget &operator=(const SessionBudget &) = delete;
  SessionBudget(SessionBudget &&) = delete;
  SessionBudget &operator=(SessionBudget &&) = delete;

private:
  const std::size_t total_bytes_;
  std::atomic<std::size_t> bytes_used_;
};

} // namespace aegis::session

#endif // AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_BUDGET_H_
