// engine_cpp/src/inference/embedder.h
//
// Abstract embedder interface per ADR-0020 §Decision.7. One instance
// per engine process, shared across all sessions. A concrete subclass
// holds the model state:
//
//   - `GGMLEmbedder`   — default, loads bge-m3 Q4_K_M GGUF via the
//                        existing ggml runtime (lands in Phase 3b
//                        Slice 2).
//   - `RemoteEmbedder` — escape hatch for deployments that swap in a
//                        managed-inference service (SageMaker /
//                        Bedrock / hosted bge-m3). Not default; see
//                        ADR-0020 Rationale §"Why engine-owned over
//                        SageMaker / Bedrock".
//
// Thread safety: the default contract is "concrete subclasses serialize
// internally OR document required external serialization." The engine's
// gRPC server runs one session per thread (ADR-0010 Sub-decision 1), so
// concurrent Embed() calls from different sessions WILL land on a
// shared Embedder. ggml's forward-pass context is not reentrant, so
// the GGMLEmbedder implementation takes the internal-mutex route.
// `RemoteEmbedder` can skip the mutex if its transport is
// independently thread-safe.
//
// Model identity: ADR-0020 §Decision.5 pins bge-m3 Q4_K_M as the
// default embedding model. Quantization + model version are part of
// the Embedder's identity — two embedders with the same output
// Dimensions() but different quantization produce vectors in
// DIFFERENT spaces (cosine similarity ~0.97 between FP16 and Q4_K_M
// of the same text, not 1.0; this is the drift risk ADR-0019 was
// superseded over). `ModelTag()` surfaces this so:
//
//   - the seed pipeline stamps the Qdrant collection metadata with
//     which embedder wrote the vectors, and
//   - the query path asserts the live Embedder's tag matches the
//     collection's tag before a query goes out.

#ifndef AEGIS_ENGINE_CPP_SRC_INFERENCE_EMBEDDER_H_
#define AEGIS_ENGINE_CPP_SRC_INFERENCE_EMBEDDER_H_

#include <string>
#include <string_view>
#include <vector>

#include "absl/status/statusor.h"
#include "absl/types/span.h"

namespace aegis::inference {

class Embedder {
public:
  virtual ~Embedder() = default;

  // Embed a single text. Returns a unit-normalized dense vector of
  // length `Dimensions()`. Errors:
  //   InvalidArgument    — empty text
  //   Internal           — backend (ggml / remote) failure
  //   ResourceExhausted  — out-of-memory during inference
  virtual absl::StatusOr<std::vector<float>> Embed(std::string_view text) = 0;

  // Batch embed. Default implementation loops over Embed(); a
  // concrete subclass with a natively-batched backend (ggml batch
  // inference, remote batch endpoint) overrides for throughput. The
  // default preserves ordering — the i-th output corresponds to the
  // i-th input.
  virtual absl::StatusOr<std::vector<std::vector<float>>>
  EmbedBatch(absl::Span<const std::string_view> texts);

  // Output vector length. Known at construction time; cheap.
  virtual int Dimensions() const = 0;

  // Identity tag stamped into Qdrant collection metadata. Format:
  //   "<model>/<quant>/<version>"   for local embedders, e.g.
  //                                 "bge-m3/Q4_K_M/v1.0"
  //   "<service>/<endpoint>/<ver>"  for remote embedders.
  // Seed and query assert match before queries are issued; a
  // collection stamped with tag A cannot be queried by an Embedder
  // reporting tag B.
  virtual std::string_view ModelTag() const = 0;

  Embedder(const Embedder &) = delete;
  Embedder &operator=(const Embedder &) = delete;
  Embedder(Embedder &&) = delete;
  Embedder &operator=(Embedder &&) = delete;

protected:
  Embedder() = default;
};

} // namespace aegis::inference

#endif // AEGIS_ENGINE_CPP_SRC_INFERENCE_EMBEDDER_H_
