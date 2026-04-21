// engine_cpp/src/vectordb/qdrant_client.h
//
// Thin C++ wrapper over Qdrant's gRPC API (protos vendored at
// proto/qdrant/v1.17.1/). Surface is deliberately scoped to the three
// operations the RAG seed + query path needs — CreateCollection,
// UpsertPoints, Search — not the full Qdrant API. Adding more methods
// is justified per-caller when a real consumer shows up; YAGNI until
// then (CLAUDE.md "don't design for hypothetical future requirements").
//
// Thread safety: grpc::Channel and the stub are thread-safe, and the
// pimpl Impl holds them. Multiple threads may call Upsert/Search
// concurrently; serialization is gRPC-managed.
//
// Local mode (Qdrant on dev machine):
//   QDRANT_URL=localhost:6334   (plaintext gRPC, no auth)
//
// Cloud mode (Qdrant Cloud free tier):
//   QDRANT_URL=xxx.aws.cloud.qdrant.io:6334
//   QDRANT_API_KEY=<key>        (sent as `api-key` metadata header)
//   use_tls=true
//
// ConfigFromEnv() handles the env read + TLS inference; Create() opens
// the channel. Callers that need test isolation can pass an explicit
// Config.

#ifndef AEGIS_ENGINE_CPP_SRC_VECTORDB_QDRANT_CLIENT_H_
#define AEGIS_ENGINE_CPP_SRC_VECTORDB_QDRANT_CLIENT_H_

#include <map>
#include <memory>
#include <string>
#include <string_view>
#include <vector>

#include "absl/status/status.h"
#include "absl/status/statusor.h"
#include "absl/types/span.h"

namespace aegis::vectordb {

enum class DistanceMetric {
  kCosine,
  kDotProduct,
  kEuclidean,
};

// A single point to upsert. Payload values are JSON-encoded strings —
// keep it narrow. Richer payload types (typed ints, nested JSON) are
// added when a real caller needs them.
struct Point {
  // UUID string. If empty at UpsertPoints time, QdrantClient rejects —
  // we do not auto-generate IDs because deterministic IDs are what
  // lets the RAG seed path be idempotent across re-runs.
  std::string id;
  std::vector<float> vector;
  std::map<std::string, std::string> payload;
};

struct SearchResult {
  std::string id;
  float score;
  std::map<std::string, std::string> payload;
};

// Narrow read-side surface over a vector database. QdrantClient
// implements it; `aegis::rag::Retriever` depends on this — not on
// QdrantClient directly — so unit tests can inject a fake without
// pulling in grpc + qdrant protos. Interface-segregation per CLAUDE.md:
// the retriever only reads vectors, so that's the only method it sees.
class VectorSearcher {
public:
  virtual ~VectorSearcher() = default;

  virtual absl::StatusOr<std::vector<SearchResult>>
  Search(std::string_view collection, absl::Span<const float> query_vec,
         int top_k) = 0;

protected:
  VectorSearcher() = default;
  VectorSearcher(const VectorSearcher &) = delete;
  VectorSearcher &operator=(const VectorSearcher &) = delete;
};

class QdrantClient : public VectorSearcher {
public:
  struct Config {
    std::string endpoint; // host:port, no scheme
    bool use_tls = false;
    std::string api_key; // empty → no auth header
  };

  // Reads QDRANT_URL (required) + QDRANT_API_KEY (optional) from the
  // environment. Infers use_tls from the URL scheme if present:
  //   "https://host:port" → use_tls=true, strips the scheme
  //   "http://host:port"  → use_tls=false, strips the scheme
  //   "host:port"         → use_tls=false (assume local / plaintext)
  // Returns InvalidArgument if QDRANT_URL is missing or empty.
  static absl::StatusOr<Config> ConfigFromEnv();

  static absl::StatusOr<std::unique_ptr<QdrantClient>>
  Create(const Config &cfg);

  ~QdrantClient();

  QdrantClient(const QdrantClient &) = delete;
  QdrantClient &operator=(const QdrantClient &) = delete;

  // Creates a collection with a single dense vector config of the
  // given dimension and distance metric. Idempotent: if the collection
  // already exists with matching vector_dim + metric, returns OK.
  // Mismatched config returns FailedPrecondition.
  absl::Status CreateCollection(std::string_view name, int vector_dim,
                                DistanceMetric metric);

  // Upserts points into the named collection. Overwrites existing
  // points with the same ID (Qdrant's native semantics). Empty points
  // span is a no-op that returns OK. Returns InvalidArgument if any
  // point has empty id or vector with size != collection's vector_dim.
  absl::Status UpsertPoints(std::string_view collection,
                            absl::Span<const Point> points);

  // Searches for the top_k nearest points to query_vec. Results are
  // sorted by score descending. Payload fields are always returned
  // (no filtering); Phase 3b scope keeps payload small enough that
  // over-fetching is not a concern.
  absl::StatusOr<std::vector<SearchResult>>
  Search(std::string_view collection, absl::Span<const float> query_vec,
         int top_k) override;

private:
  struct Impl;
  explicit QdrantClient(std::unique_ptr<Impl> impl);
  std::unique_ptr<Impl> impl_;
};

} // namespace aegis::vectordb

#endif // AEGIS_ENGINE_CPP_SRC_VECTORDB_QDRANT_CLIENT_H_
