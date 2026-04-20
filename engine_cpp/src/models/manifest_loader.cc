// engine_cpp/src/models/manifest_loader.cc

#include "engine_cpp/src/models/manifest_loader.h"

#include <sys/stat.h>

#include <cstdio>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "absl/strings/str_join.h"
#include "absl/strings/string_view.h"
#include "nlohmann/json.hpp"
#include "openssl/sha.h"

namespace aegis::models {

namespace {

// Hex-encode a byte buffer into lowercase ASCII.
std::string HexEncode(const unsigned char *bytes, std::size_t len) {
  static constexpr char kHex[] = "0123456789abcdef";
  std::string out(len * 2, '\0');
  for (std::size_t i = 0; i < len; ++i) {
    out[i * 2] = kHex[bytes[i] >> 4];
    out[i * 2 + 1] = kHex[bytes[i] & 0x0f];
  }
  return out;
}

// Extract the last '.'-delimited suffix of a filename (including the dot).
// "ggml-tiny.en.bin" → ".bin"; "foo" → ""; ".bashrc" → ".bashrc".
std::string Extension(absl::string_view filename) {
  const std::size_t dot = filename.rfind('.');
  if (dot == absl::string_view::npos) {
    return "";
  }
  return std::string(filename.substr(dot));
}

} // namespace

std::string ResolveCasPath(const std::string &model_root,
                           const ModelEntry &entry) {
  // <root>/<id>/<sha>.<ext> — per ADR-0026 §Storage layout.
  return absl::StrCat(model_root, "/", entry.id, "/", entry.sha256,
                      Extension(entry.filename));
}

std::string ComputeFileSha256(const std::string &path) {
  std::ifstream f(path, std::ios::binary);
  if (!f.good()) {
    return "";
  }

  SHA256_CTX ctx;
  SHA256_Init(&ctx);

  // 64 KiB buffer — balances syscall cost against memory.
  // ~10 s/GB on typical SSD throughput; acceptable startup tax per
  // ADR-0026 §Revision 2026-04-19 (second pivot).
  static constexpr std::size_t kBufSize = 64 * 1024;
  std::vector<char> buf(kBufSize);
  while (f.good()) {
    f.read(buf.data(), static_cast<std::streamsize>(buf.size()));
    const std::streamsize n = f.gcount();
    if (n > 0) {
      SHA256_Update(&ctx, buf.data(), static_cast<std::size_t>(n));
    }
  }
  if (f.bad()) {
    return "";
  }

  unsigned char digest[SHA256_DIGEST_LENGTH];
  SHA256_Final(digest, &ctx);
  return HexEncode(digest, SHA256_DIGEST_LENGTH);
}

absl::StatusOr<Manifest> LoadManifest(const std::string &manifest_path) {
  std::ifstream f(manifest_path);
  if (!f.good()) {
    return absl::NotFoundError(absl::StrCat(
        "manifest_loader: cannot open manifest at ", manifest_path));
  }

  nlohmann::json j;
  try {
    f >> j;
  } catch (const nlohmann::json::parse_error &e) {
    return absl::InvalidArgumentError(
        absl::StrCat("manifest_loader: JSON parse error in ", manifest_path,
                     ": ", e.what()));
  }

  Manifest m;
  // schema_version is required at the top level.
  if (!j.contains("schema_version") ||
      !j["schema_version"].is_number_integer()) {
    return absl::InvalidArgumentError(
        "manifest_loader: missing or non-integer `schema_version`");
  }
  m.schema_version = j["schema_version"].get<int>();

  if (!j.contains("models") || !j["models"].is_array()) {
    return absl::InvalidArgumentError(
        "manifest_loader: missing or non-array `models`");
  }

  for (const auto &e : j["models"]) {
    ModelEntry entry;
    auto get_str = [&](const char *key, std::string *out) -> absl::Status {
      if (!e.contains(key) || !e[key].is_string()) {
        return absl::InvalidArgumentError(absl::StrCat(
            "manifest_loader: entry missing/non-string field `", key, "`"));
      }
      *out = e[key].get<std::string>();
      return absl::OkStatus();
    };

    if (auto s = get_str("id", &entry.id); !s.ok())
      return s;
    if (auto s = get_str("filename", &entry.filename); !s.ok())
      return s;
    if (auto s = get_str("type", &entry.type); !s.ok())
      return s;
    if (auto s = get_str("sha256", &entry.sha256); !s.ok())
      return s;

    if (!e.contains("size_bytes") || !e["size_bytes"].is_number_integer()) {
      return absl::InvalidArgumentError(
          absl::StrCat("manifest_loader: entry `", entry.id,
                       "` missing/non-integer `size_bytes`"));
    }
    entry.size_bytes = e["size_bytes"].get<std::int64_t>();

    if (!e.contains("required") || !e["required"].is_boolean()) {
      return absl::InvalidArgumentError(
          absl::StrCat("manifest_loader: entry `", entry.id,
                       "` missing/non-boolean `required`"));
    }
    entry.required = e["required"].get<bool>();

    m.models.push_back(std::move(entry));
  }

  return m;
}

VerifyResult VerifyEntry(const std::string &model_root,
                         const ModelEntry &entry) {
  VerifyResult r;
  r.id = entry.id;
  r.path = ResolveCasPath(model_root, entry);

  // Layer 1 — stat (exists).
  struct stat st {};
  if (::stat(r.path.c_str(), &st) != 0) {
    r.ok = false;
    r.diagnostic = absl::StrCat(
        "model `", entry.id, "` MISSING: expected CAS path `", r.path,
        "` does not exist. Populate the CAS layout via "
        "`./tools/scripts/download_models.sh` (LAN) or the CI "
        "`release-staging-image.yml` workflow's populate step (Cloud).");
    return r;
  }

  // Layer 2 — size match.
  const std::int64_t actual_size = static_cast<std::int64_t>(st.st_size);
  if (actual_size != entry.size_bytes) {
    r.ok = false;
    r.diagnostic = absl::StrCat(
        "model `", entry.id, "` WRONG SIZE at `", r.path, "`: expected ",
        entry.size_bytes, " bytes, found ", actual_size,
        " bytes. Likely partial-write during populate, or populator"
        " wrote wrong content at CAS path (CAS invariant violated —"
        " re-populate from upstream per manifest `origin_url`).");
    return r;
  }

  // Layer 3 — SHA-256 recompute.
  // Trust-by-construction (filename IS the SHA) is deliberately rejected
  // per ADR-0026 §Revision 2026-04-19 — defense-in-depth against bit rot,
  // silent S3 corruption, compromised populator. ~10 s/GB.
  const std::string actual_sha = ComputeFileSha256(r.path);
  if (actual_sha.empty()) {
    r.ok = false;
    r.diagnostic = absl::StrCat(
        "model `", entry.id, "` I/O ERROR computing SHA-256 at `", r.path,
        "`. Filesystem error during read; re-mount model storage or "
        "restart pod.");
    return r;
  }
  if (actual_sha != entry.sha256) {
    r.ok = false;
    r.diagnostic = absl::StrCat(
        "model `", entry.id, "` SHA-256 MISMATCH at `", r.path, "`: expected ",
        entry.sha256, ", computed ", actual_sha,
        ". This means file bytes do not match CAS invariant — "
        "bit rot, partial-write, or compromised populator. DO NOT "
        "proceed; re-populate from upstream per manifest `origin_url`.");
    return r;
  }

  r.ok = true;
  return r;
}

absl::Status VerifyAllRequired(const std::string &model_root,
                               const Manifest &manifest) {
  std::vector<std::string> failures;
  bool any_missing = false;
  bool any_bad_content = false;

  for (const auto &entry : manifest.models) {
    if (!entry.required) {
      continue;
    }
    VerifyResult r = VerifyEntry(model_root, entry);
    if (r.ok) {
      continue;
    }
    failures.push_back(r.diagnostic);
    // Classify for aggregate error code: missing vs corruption.
    if (r.diagnostic.find("MISSING") != std::string::npos) {
      any_missing = true;
    } else {
      any_bad_content = true;
    }
  }

  if (failures.empty()) {
    return absl::OkStatus();
  }

  const std::string aggregate =
      absl::StrCat("manifest_loader: ", failures.size(),
                   " required model(s) failed CAS preflight under root `",
                   model_root, "`:\n  - ", absl::StrJoin(failures, "\n  - "));

  // Corruption wins the classification — it's the more alarming signal.
  if (any_bad_content) {
    return absl::DataLossError(aggregate);
  }
  if (any_missing) {
    return absl::NotFoundError(aggregate);
  }
  return absl::InternalError(aggregate);
}

} // namespace aegis::models
