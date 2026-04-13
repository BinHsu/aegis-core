// engine_cpp/src/infra/sensitive_bytes.h
//
// ADR-0005 R3 enforcement — compile-time type guard against audio PCM
// leaking into logs, traces, or other observability surfaces.
//
// Design:
//   - `SensitiveBytes` is a non-owning view of a raw byte span.
//   - The ONLY way to stream it to an ostream is the safe overload
//     that emits "[REDACTED Nbytes]" — never the bytes themselves.
//   - There is no implicit conversion to std::string, std::span, or
//     any other loggable type.
//   - Code that legitimately needs the raw bytes (whisper.cpp inference,
//     gRPC framing) calls `.bytes()` explicitly — this is a searchable
//     call site that code review can audit.
//
// Semgrep rule in tools/ci/semgrep_rules/ (TODO) flags any misuse of
// `.bytes()` outside the whitelisted inference call sites.

#ifndef AEGIS_ENGINE_CPP_SRC_INFRA_SENSITIVE_BYTES_H_
#define AEGIS_ENGINE_CPP_SRC_INFRA_SENSITIVE_BYTES_H_

#include <cstddef>
#include <cstdint>
#include <ostream>
#include <span>

namespace aegis::infra {

class SensitiveBytes {
 public:
  constexpr SensitiveBytes() noexcept = default;

  // Caller is responsible for ensuring the underlying buffer outlives
  // the SensitiveBytes view (typically the ServerReaderWriter-owned
  // IngestMessage or a Session-owned ring buffer).
  explicit constexpr SensitiveBytes(std::span<const std::byte> data) noexcept
      : data_(data) {}

  // Convenience ctor from raw pointer + size (for proto::bytes fields
  // which come as std::string-of-char).
  SensitiveBytes(const void* data, std::size_t size) noexcept
      : data_(static_cast<const std::byte*>(data), size) {}

  // Explicit raw access. Code that needs the bytes for legitimate
  // purposes (inference, framing) uses this. This is the ONE call
  // site that CI (Semgrep) audits for ADR-0005 compliance.
  constexpr std::span<const std::byte> bytes() const noexcept { return data_; }

  constexpr std::size_t size() const noexcept { return data_.size(); }
  constexpr bool empty() const noexcept { return data_.empty(); }

  // Copy/move are safe because the view is non-owning. Clients must
  // ensure the underlying buffer lifetime covers the view.
  constexpr SensitiveBytes(const SensitiveBytes&) noexcept = default;
  constexpr SensitiveBytes& operator=(const SensitiveBytes&) noexcept = default;
  constexpr SensitiveBytes(SensitiveBytes&&) noexcept = default;
  constexpr SensitiveBytes& operator=(SensitiveBytes&&) noexcept = default;

 private:
  std::span<const std::byte> data_{};
};

// The ONLY stream operator for SensitiveBytes. Always emits the
// redacted form. There is no escape hatch that prints the bytes.
inline std::ostream& operator<<(std::ostream& os, const SensitiveBytes& sb) {
  return os << "[REDACTED " << sb.size() << " bytes]";
}

}  // namespace aegis::infra

#endif  // AEGIS_ENGINE_CPP_SRC_INFRA_SENSITIVE_BYTES_H_
