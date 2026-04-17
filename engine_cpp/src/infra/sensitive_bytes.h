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

#include <concepts>
#include <cstddef>
#include <cstdint>
#include <ostream>
#include <span>
#include <type_traits>

namespace aegis::infra {

// ByteLikePointee constrains the raw-pointer `SensitiveBytes` ctor to
// types whose pointee is "actually a byte" in the sense of the gRPC
// wire framing this codebase uses:
//
//   - `char`           — `proto::bytes` fields decode to `std::string`,
//                        whose `.data()` returns `const char*`.
//   - `unsigned char`  — the C idiom for raw buffers.
//   - `std::byte`      — the C++17 "explicitly opaque binary data"
//                        type; what we would write on a greenfield API.
//   - `std::uint8_t`   — fixed-width byte alias, common in codec code.
//
// This is a C++20 `concept` (§7.5) used as a template parameter
// constraint. It replaces a `void*` parameter that accepted anything,
// including pointers into wildly inappropriate types (a `TranscriptSegment*`
// would have compiled silently). With the constraint in place, a
// mis-use is rejected at the point of construction with a readable
// compiler diagnostic naming the concept, not a dump of template
// instantiation context.
//
// Rationale for using a concept here specifically: the compile-time
// type system is already carrying the "sensitive-data" invariant (ADR-
// 0005 R3); the concept extends that discipline to the entry point
// where raw pointers originate (proto field decoding). Future Semgrep
// rules can then audit only the `.bytes()` exit point — the entry
// point is self-auditing via the compiler.
template <typename T>
concept ByteLikePointee = std::same_as<std::remove_cv_t<T>, char> ||
                          std::same_as<std::remove_cv_t<T>, unsigned char> ||
                          std::same_as<std::remove_cv_t<T>, std::byte> ||
                          std::same_as<std::remove_cv_t<T>, std::uint8_t>;

class SensitiveBytes {
public:
  constexpr SensitiveBytes() noexcept = default;

  // Caller is responsible for ensuring the underlying buffer outlives
  // the SensitiveBytes view (typically the ServerReaderWriter-owned
  // IngestMessage or a Session-owned ring buffer).
  explicit constexpr SensitiveBytes(std::span<const std::byte> data) noexcept
      : data_(data) {}

  // Raw-pointer ctor for proto::bytes callers. Constrained by
  // `ByteLikePointee` above so the compiler rejects, at the call site,
  // any pointee that isn't one of the four byte-sized types we treat
  // interchangeably for wire framing. `reinterpret_cast` is necessary
  // because `std::span<const std::byte>` cannot be constructed directly
  // from `const char*`; the concept guarantees the reinterpret is
  // well-defined (all four types have size 1 and byte-like alignment).
  template <ByteLikePointee T>
  SensitiveBytes(const T *data, std::size_t size) noexcept
      : data_(reinterpret_cast<const std::byte *>(data), size) {}

  // Explicit raw access. Code that needs the bytes for legitimate
  // purposes (inference, framing) uses this. This is the ONE call
  // site that CI (Semgrep) audits for ADR-0005 compliance.
  constexpr std::span<const std::byte> bytes() const noexcept { return data_; }

  constexpr std::size_t size() const noexcept { return data_.size(); }
  constexpr bool empty() const noexcept { return data_.empty(); }

  // Copy/move are safe because the view is non-owning. Clients must
  // ensure the underlying buffer lifetime covers the view.
  constexpr SensitiveBytes(const SensitiveBytes &) noexcept = default;
  constexpr SensitiveBytes &
  operator=(const SensitiveBytes &) noexcept = default;
  constexpr SensitiveBytes(SensitiveBytes &&) noexcept = default;
  constexpr SensitiveBytes &operator=(SensitiveBytes &&) noexcept = default;

private:
  std::span<const std::byte> data_{};
};

// The ONLY stream operator for SensitiveBytes. Always emits the
// redacted form. There is no escape hatch that prints the bytes.
inline std::ostream &operator<<(std::ostream &os, const SensitiveBytes &sb) {
  return os << "[REDACTED " << sb.size() << " bytes]";
}

} // namespace aegis::infra

#endif // AEGIS_ENGINE_CPP_SRC_INFRA_SENSITIVE_BYTES_H_
