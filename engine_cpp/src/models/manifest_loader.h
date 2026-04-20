// engine_cpp/src/models/manifest_loader.h
//
// CAS preflight walker for `models/manifest.json` — ADR-0026 §Engine
// responsibilities. At engine startup, before the gRPC server binds,
// every `required: true` manifest entry is verified against the on-disk
// CAS layout:
//
//     $AEGIS_MODEL_PATH / <id> / <sha256-hex>.<ext>
//
// Verification is three-layered per ADR-0026 §Revision 2026-04-19:
//   (1) stat — file exists
//   (2) size — byte count matches manifest's `size_bytes`
//   (3) SHA-256 — recompute + compare to manifest's `sha256`
//
// Any failure is fatal + operator-readable. Extra files on disk (belonging
// to concurrently-deployed engine versions, per rolling-deploy design)
// are silently tolerated.

#ifndef AEGIS_ENGINE_CPP_SRC_MODELS_MANIFEST_LOADER_H_
#define AEGIS_ENGINE_CPP_SRC_MODELS_MANIFEST_LOADER_H_

#include <cstdint>
#include <string>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"

namespace aegis::models {

// Parsed representation of one entry from models/manifest.json.
// Fields omitted from this struct (origin_project, license, phase, notes,
// …) are not load-bearing for the walker — kept out to reduce coupling
// to manifest schema evolution.
struct ModelEntry {
  std::string id;       // lowercase alnum+hyphens; used as CAS subdir
  std::string filename; // upstream filename; used for extension only
  std::string type;     // "transcription" | "diarization" | "embedding" | "llm"
  std::string sha256;   // lowercase 64-hex; CAS path component
  std::int64_t size_bytes; // expected file size
  bool required;           // walker iterates `required` entries only
};

struct Manifest {
  int schema_version = 0;
  std::vector<ModelEntry> models;
};

// Parse a manifest.json file.
absl::StatusOr<Manifest> LoadManifest(const std::string &manifest_path);

// Compute the CAS path for an entry: <root>/<id>/<sha256>.<ext>
// where <ext> is the last '.'-suffix of `entry.filename` (including the dot).
std::string ResolveCasPath(const std::string &model_root,
                           const ModelEntry &entry);

// Compute the SHA-256 of a file, streaming — does not load the whole file
// into memory. Returns lowercase hex string. Empty string on I/O error.
std::string ComputeFileSha256(const std::string &path);

// Per-entry verification result. On failure, `diagnostic` contains the
// operator-readable message (missing / wrong-size / wrong-sha).
struct VerifyResult {
  std::string id;
  std::string path;
  bool ok = false;
  std::string diagnostic;
};

// Verify a single entry: stat → size → SHA-256. Stops at the first failing
// layer and reports it. Callers only invoke this on `entry.required==true`.
VerifyResult VerifyEntry(const std::string &model_root,
                         const ModelEntry &entry);

// Walk every `required: true` entry in the manifest. Returns ok Status
// when all pass; otherwise NotFoundError (any missing) or DataLossError
// (size or SHA mismatch — implies disk corruption or wrong content at
// right path) with an aggregated multi-line diagnostic.
//
// Extra files on disk (other engine versions' models) are silently
// tolerated — the walker only asks "are my required entries present
// with correct content?", not "is the bucket exactly what I expect?".
absl::Status VerifyAllRequired(const std::string &model_root,
                               const Manifest &manifest);

} // namespace aegis::models

#endif // AEGIS_ENGINE_CPP_SRC_MODELS_MANIFEST_LOADER_H_
