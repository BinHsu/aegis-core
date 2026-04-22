// engine_cpp/cmd/engine/seed.cc

#include "engine_cpp/cmd/engine/seed.h"

#include <cctype>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <iostream>
#include <memory>
#include <sstream>
#include <string>
#include <string_view>
#include <vector>

#include "absl/flags/flag.h"
#include "absl/flags/parse.h"
#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/strings/str_cat.h"
#include "absl/strings/str_format.h"
#include "engine_cpp/src/inference/ggml_embedder.h"
#include "engine_cpp/src/models/manifest_loader.h"
#include "engine_cpp/src/rag/chunker.h"
#include "engine_cpp/src/vectordb/qdrant_client.h"
#include "openssl/sha.h"

ABSL_FLAG(std::string, corpus, "",
          "Path to the markdown corpus to seed. Required.");
ABSL_FLAG(std::string, target, "local",
          "Where to write vectors: 'local' (localhost:6334, plaintext) or "
          "'cloud' (reads QDRANT_URL + QDRANT_API_KEY from env).");
ABSL_FLAG(std::string, tenant, "demo",
          "Tenant namespace for collection naming (ADR-0022). The seeded "
          "collection is named `aegis_<tenant>_<corpus-stem>`. LAN demo "
          "defaults to 'demo'; cloud seed jobs should pass the owning "
          "tenant (JWT sub) explicitly.");
ABSL_FLAG(bool, verbose, false,
          "Print per-chunk progress to stderr. Default is silent on success.");

namespace aegis::engine_cmd {

// -----------------------------------------------------------------------------
// Public helpers (declared in seed.h, used by the subcommand and tests).
// -----------------------------------------------------------------------------

std::string ContentHashUuid(std::string_view text) {
  unsigned char hash[SHA256_DIGEST_LENGTH];
  SHA256(reinterpret_cast<const unsigned char *>(text.data()), text.size(),
         hash);
  // Take first 16 bytes. Set version-5 nibble + RFC 4122 variant bits
  // so Qdrant (and any other UUID parser) treats the string as a
  // well-formed UUID. Using SHA-256 instead of SHA-1 is an
  // intentional divergence from strict UUID5 — the project already
  // depends on SHA-256 everywhere else, and the RFC 4122 formatting
  // is what matters for Qdrant's acceptance, not the particular
  // hash function.
  unsigned char u[16];
  std::memcpy(u, hash, 16);
  u[6] = (u[6] & 0x0f) | 0x50; // version = 5
  u[8] = (u[8] & 0x3f) | 0x80; // variant = 10xx
  return absl::StrFormat(
      "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
      u[0], u[1], u[2], u[3], u[4], u[5], u[6], u[7], u[8], u[9], u[10], u[11],
      u[12], u[13], u[14], u[15]);
}

std::string DeriveCollectionName(std::string_view corpus_path,
                                 std::string_view tenant) {
  // Take basename (strip directory) then strip trailing extension.
  size_t slash = corpus_path.find_last_of('/');
  std::string_view base = slash == std::string_view::npos
                              ? corpus_path
                              : corpus_path.substr(slash + 1);
  size_t dot = base.find_last_of('.');
  std::string_view stem =
      dot == std::string_view::npos ? base : base.substr(0, dot);

  auto sanitize = [](std::string_view in) {
    std::string out;
    out.reserve(in.size());
    for (char c : in) {
      if ((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
        out.push_back(c);
      } else if (c >= 'A' && c <= 'Z') {
        out.push_back(static_cast<char>(c + ('a' - 'A')));
      } else {
        out.push_back('_');
      }
    }
    return out;
  };

  // Sanitize: allow [a-z0-9_], lowercase A–Z, any other character
  // becomes '_'. Qdrant accepts a broader charset but staying
  // conservative keeps the name safe for URL-style references and
  // shell quoting. Same rule applies to the tenant segment so a
  // hostile / malformed `--tenant` value cannot inject `_`-separated
  // characters that break the `aegis_<tenant>_<stem>` prefix parse.
  std::string sanitized_stem = sanitize(stem);
  if (sanitized_stem.empty()) {
    sanitized_stem = "unnamed";
  }
  std::string sanitized_tenant = sanitize(tenant);
  if (sanitized_tenant.empty()) {
    sanitized_tenant = "demo";
  }
  return absl::StrCat("aegis_", sanitized_tenant, "_", sanitized_stem);
}

namespace {

constexpr int kBgeM3Dim = 1024;
constexpr const char *kBgeM3ModelId = "bge-m3-q4km";

// -----------------------------------------------------------------------------
// File + env helpers (internal to the seed pipeline).
// -----------------------------------------------------------------------------

absl::StatusOr<std::string> ReadFile(const std::string &path) {
  std::ifstream f(path, std::ios::binary);
  if (!f.good()) {
    return absl::NotFoundError(
        absl::StrCat("seed: cannot open corpus file: ", path));
  }
  std::stringstream ss;
  ss << f.rdbuf();
  return ss.str();
}

// Resolve the bge-m3 embedder's on-disk path via the manifest + CAS layout
// (ADR-0026). Prefers AEGIS_MANIFEST_PATH + AEGIS_MODEL_PATH (root) — the
// same contract engine main uses. Falls back to repo-relative defaults
// (`models/manifest.json` + `models/`) so `bazel run` and CI test fixtures
// both work without extra env plumbing.
absl::StatusOr<std::string> ResolveEmbedderModelPath() {
  std::string manifest_path = "models/manifest.json";
  if (const char *env = std::getenv("AEGIS_MANIFEST_PATH"); env != nullptr) {
    manifest_path = env;
  }
  std::string model_root = "models";
  if (const char *env = std::getenv("AEGIS_MODEL_PATH"); env != nullptr) {
    model_root = env;
  }

  auto manifest_or = aegis::models::LoadManifest(manifest_path);
  if (!manifest_or.ok()) {
    return manifest_or.status();
  }
  for (const auto &e : manifest_or->models) {
    if (e.id == kBgeM3ModelId) {
      return aegis::models::ResolveCasPath(model_root, e);
    }
  }
  return absl::NotFoundError(absl::StrCat("seed: manifest at `", manifest_path,
                                          "` has no entry with id=`",
                                          kBgeM3ModelId, "`"));
}

// -----------------------------------------------------------------------------
// QdrantClient construction per --target.
// -----------------------------------------------------------------------------

absl::StatusOr<std::unique_ptr<vectordb::QdrantClient>>
BuildQdrantClient(const std::string &target) {
  if (target == "local") {
    vectordb::QdrantClient::Config cfg;
    cfg.endpoint = "localhost:6334";
    cfg.use_tls = false;
    // No api_key for local — Qdrant's default local install has no auth.
    return vectordb::QdrantClient::Create(cfg);
  }
  if (target == "cloud") {
    auto cfg = vectordb::QdrantClient::ConfigFromEnv();
    if (!cfg.ok()) {
      return cfg.status();
    }
    return vectordb::QdrantClient::Create(*cfg);
  }
  return absl::InvalidArgumentError(absl::StrCat(
      "seed: --target must be 'local' or 'cloud', got '", target, "'"));
}

} // namespace

// -----------------------------------------------------------------------------
// RunSeed entry point
// -----------------------------------------------------------------------------

int RunSeed(int argc, char **argv) {
  absl::ParseCommandLine(argc, argv);

  const std::string corpus_path = absl::GetFlag(FLAGS_corpus);
  const std::string target = absl::GetFlag(FLAGS_target);
  const std::string tenant = absl::GetFlag(FLAGS_tenant);
  const bool verbose = absl::GetFlag(FLAGS_verbose);

  if (corpus_path.empty()) {
    std::cerr << "seed: --corpus is required\n";
    return EXIT_FAILURE;
  }

  // Stage 1 — read the corpus file.
  auto corpus = ReadFile(corpus_path);
  if (!corpus.ok()) {
    std::cerr << corpus.status() << "\n";
    return EXIT_FAILURE;
  }

  // Stage 2 — chunk with ADR-0019 defaults.
  rag::MarkdownChunker chunker;
  const std::vector<rag::Chunk> chunks = chunker.Split(*corpus);
  if (chunks.empty()) {
    std::cerr << "seed: corpus produced zero chunks; nothing to seed\n";
    return EXIT_FAILURE;
  }
  if (verbose) {
    std::cerr << "seed: " << chunks.size() << " chunks from " << corpus_path
              << "\n";
  }

  // Stage 3 — load the embedder (bge-m3 Q4_K_M via GGMLEmbedder).
  auto model_path_or = ResolveEmbedderModelPath();
  if (!model_path_or.ok()) {
    std::cerr << "seed: " << model_path_or.status() << "\n";
    return EXIT_FAILURE;
  }
  const std::string model_path = *model_path_or;
  auto embedder = inference::GGMLEmbedder::Create(model_path);
  if (!embedder.ok()) {
    std::cerr << "seed: " << embedder.status() << "\n";
    return EXIT_FAILURE;
  }
  if ((*embedder)->Dimensions() != kBgeM3Dim) {
    std::cerr << "seed: embedder dim=" << (*embedder)->Dimensions()
              << " does not match expected bge-m3 dim=" << kBgeM3Dim << "\n";
    return EXIT_FAILURE;
  }

  // Stage 4 — open the Qdrant client.
  auto client = BuildQdrantClient(target);
  if (!client.ok()) {
    std::cerr << "seed: " << client.status() << "\n";
    return EXIT_FAILURE;
  }

  // Stage 5 — create the collection (idempotent via CollectionExists
  // fast-path in QdrantClient).
  const std::string collection = DeriveCollectionName(corpus_path, tenant);
  const auto create_status = (*client)->CreateCollection(
      collection, kBgeM3Dim, vectordb::DistanceMetric::kCosine);
  if (!create_status.ok()) {
    std::cerr << "seed: " << create_status << "\n";
    return EXIT_FAILURE;
  }
  if (verbose) {
    std::cerr << "seed: collection='" << collection << "' ready\n";
  }

  // Stage 6 — embed every chunk + build Point batch.
  std::vector<vectordb::Point> points;
  points.reserve(chunks.size());
  for (size_t i = 0; i < chunks.size(); ++i) {
    auto vec = (*embedder)->Embed(chunks[i].text);
    if (!vec.ok()) {
      std::cerr << "seed: embed failed on chunk " << i << ": " << vec.status()
                << "\n";
      return EXIT_FAILURE;
    }
    vectordb::Point p;
    p.id = ContentHashUuid(chunks[i].text);
    p.vector = std::move(*vec);
    p.payload = {
        {"text", chunks[i].text},
        {"source_path", corpus_path},
        {"chunk_index", std::to_string(i)},
    };
    points.push_back(std::move(p));
    if (verbose) {
      std::cerr << "seed: embedded " << (i + 1) << "/" << chunks.size() << "\n";
    }
  }

  // Stage 7 — upsert the batch in one RPC. QdrantClient sets wait=true
  // so this blocks until Qdrant persists.
  const auto upsert_status = (*client)->UpsertPoints(collection, points);
  if (!upsert_status.ok()) {
    std::cerr << "seed: " << upsert_status << "\n";
    return EXIT_FAILURE;
  }

  std::cout << "seed: ok — " << points.size() << " chunks upserted into '"
            << collection << "' (target=" << target << ")\n";
  return EXIT_SUCCESS;
}

} // namespace aegis::engine_cmd
