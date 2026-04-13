// engine_cpp/src/session/resource_budget.h
//
// ADR-0010 Sub-decision 2 — prevents OOM by tracking total allocated
// bytes across all sessions and rejecting new sessions that would
// exceed the configured ceiling. Rejection surfaces as
// gRPC::StatusCode::RESOURCE_EXHAUSTED at the RPC boundary.
//
// Thread-safe via std::atomic. Reserve() / Release() must be paired
// 1:1 — typically via RAII in a session-scoped scope guard.

#ifndef AEGIS_ENGINE_CPP_SRC_SESSION_RESOURCE_BUDGET_H_
#define AEGIS_ENGINE_CPP_SRC_SESSION_RESOURCE_BUDGET_H_

#include <atomic>
#include <cstddef>

#include "absl/status/status.h"

namespace aegis::session {

class ResourceBudget {
 public:
  explicit ResourceBudget(std::size_t total_bytes) noexcept;

  // Try to reserve `bytes`. Returns OK on success; ResourceExhaustedError
  // if the reservation would exceed the budget. Over-commit is forbidden.
  absl::Status Reserve(std::size_t bytes);

  // Release a prior reservation. Must be paired 1:1 with Reserve().
  // Never fails.
  void Release(std::size_t bytes) noexcept;

  // Observability — exported as Prometheus metric
  // `aegis_engine_budget_bytes_used` per ARCH §10.6.
  std::size_t BytesUsed() const noexcept;
  std::size_t BytesAvailable() const noexcept;
  std::size_t TotalBytes() const noexcept { return total_bytes_; }

  // Non-copyable / non-movable — ResourceBudget is a process singleton.
  ResourceBudget(const ResourceBudget&) = delete;
  ResourceBudget& operator=(const ResourceBudget&) = delete;
  ResourceBudget(ResourceBudget&&) = delete;
  ResourceBudget& operator=(ResourceBudget&&) = delete;

 private:
  const std::size_t total_bytes_;
  std::atomic<std::size_t> bytes_used_;
};

}  // namespace aegis::session

#endif  // AEGIS_ENGINE_CPP_SRC_SESSION_RESOURCE_BUDGET_H_
