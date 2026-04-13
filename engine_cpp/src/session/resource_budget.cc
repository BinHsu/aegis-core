// engine_cpp/src/session/resource_budget.cc

#include "engine_cpp/src/session/resource_budget.h"

#include "absl/strings/str_cat.h"

namespace aegis::session {

ResourceBudget::ResourceBudget(std::size_t total_bytes) noexcept
    : total_bytes_(total_bytes), bytes_used_(0) {}

absl::Status ResourceBudget::Reserve(std::size_t bytes) {
  std::size_t current = bytes_used_.load(std::memory_order_acquire);
  while (true) {
    const std::size_t next = current + bytes;
    // Check for overflow AND budget overflow in one conditional.
    if (next < current || next > total_bytes_) {
      return absl::ResourceExhaustedError(absl::StrCat(
          "ResourceBudget: cannot reserve ", bytes,
          " bytes (used=", current, ", total=", total_bytes_, ")"));
    }
    if (bytes_used_.compare_exchange_weak(current, next,
                                          std::memory_order_acq_rel,
                                          std::memory_order_acquire)) {
      return absl::OkStatus();
    }
    // CAS failed — `current` was updated by the call. Loop and retry.
  }
}

void ResourceBudget::Release(std::size_t bytes) noexcept {
  bytes_used_.fetch_sub(bytes, std::memory_order_acq_rel);
}

std::size_t ResourceBudget::BytesUsed() const noexcept {
  return bytes_used_.load(std::memory_order_acquire);
}

std::size_t ResourceBudget::BytesAvailable() const noexcept {
  const std::size_t used = bytes_used_.load(std::memory_order_acquire);
  return used >= total_bytes_ ? 0 : (total_bytes_ - used);
}

}  // namespace aegis::session
