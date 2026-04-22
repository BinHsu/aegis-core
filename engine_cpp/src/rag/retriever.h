// engine_cpp/src/rag/retriever.h
//
// RAG query-side counterpart to `engine seed` (engine_cpp/cmd/engine/
// seed.cc). Given a live transcript fragment, embed it through the
// process-scoped Embedder, search the Qdrant collection bound to the
// session via `SessionStart.rag_id`, and compose a `PrompterHint` for
// the host UI's hint panel.
//
// Owned per-session: the hint_id counter is session-local monotonic
// starting at 1, matching the `TranscriptSegment.segment_id` convention
// (see `Session::Run`). This also means the retriever is single-threaded
// by construction — the engine's gRPC server runs one session per
// thread (ADR-0010 Sub-decision 1), so the non-atomic counter is fine.
//
// Dependencies are injected as raw pointers (non-owning):
//   - `Embedder*` — process-scoped (ADR-0020 §Decision.7; one
//                   GGMLEmbedder instance shared across sessions,
//                   serializes internally)
//   - `VectorSearcher*` — process-scoped (QdrantClient is thread-safe
//                         via grpc::Channel)
//
// Rationale for not depending on QdrantClient directly: `VectorSearcher`
// is the narrow read-side interface. Unit tests inject a fake that
// returns canned `SearchResult` batches without pulling in the grpc
// + qdrant-proto link stack. See `retriever_test.cc`.

#ifndef AEGIS_ENGINE_CPP_SRC_RAG_RETRIEVER_H_
#define AEGIS_ENGINE_CPP_SRC_RAG_RETRIEVER_H_

#include <cstddef>
#include <cstdint>
#include <string>
#include <string_view>

#include "absl/status/statusor.h"
#include "engine_cpp/src/inference/embedder.h"
#include "engine_cpp/src/vectordb/qdrant_client.h"
#include "proto/aegis/v1/aegis.pb.h"

namespace aegis::rag {

class Retriever {
public:
  struct Config {
    // Number of nearest-neighbor matches to pull from Qdrant per call.
    // Top-1 feeds the suggestion; the rest become `RagCitation` rows
    // on the same hint for UI transparency.
    int top_k = 3;

    // Clip `suggestion` and each citation `quote` to this many bytes.
    // Prompter UI keeps hints compact — longer spans degrade the
    // signal. 240 bytes ≈ 80 CJK characters or 240 ASCII characters,
    // matches the chunker's overlap window (ADR-0019 §Decision.2).
    std::size_t excerpt_bytes = 240;

    // Minimum cosine-similarity score for a hint to be emitted.
    // Qdrant's top-K search always returns K results when the
    // collection has ≥ K points, regardless of how far off-topic the
    // query is — without this threshold every transcript window
    // fires a hint even when the speaker says "um, let me think" and
    // the viewer UI overwrites with garbage. 0.42 is conservative
    // for bge-m3 multilingual (zh-TW tokens are semantic-dense; the
    // 0.40–0.50 band is industry-standard "meaningful match" vs
    // "random noise"). Tune higher to be stricter; set to 0 to
    // disable the gate entirely (pre-gate behaviour for regression
    // bisection).
    float min_score = 0.42f;
  };

  // `embedder` and `searcher` must outlive this Retriever. `collection`
  // is the Qdrant collection name bound via `SessionStart.rag_id` —
  // typically `aegis_<corpus_stem>` per `seed::DeriveCollectionName`.
  //
  // Two overloads instead of `Config config = {}`: the default-argument
  // form would need `Config`'s in-class initializers visible at the
  // point of declaration, which C++ doesn't guarantee for a struct
  // nested in the same class.
  Retriever(inference::Embedder *embedder, vectordb::VectorSearcher *searcher,
            std::string collection) noexcept;
  Retriever(inference::Embedder *embedder, vectordb::VectorSearcher *searcher,
            std::string collection, Config config) noexcept;

  // Embed `transcript_text`, search `collection_` for top-K matches,
  // return a PrompterHint with a session-local monotonic `hint_id`
  // starting at 1.
  //
  // Status codes:
  //   NotFound         — empty transcript; OR search returned zero
  //                      results; OR top match scored below
  //                      `config_.min_score`; OR top match is the
  //                      same Qdrant point id as the previous
  //                      emission (consecutive-same-topic dedupe).
  //                      Caller treats as "no hint to emit"; session
  //                      continues.
  //   InvalidArgument  — embedder produced an empty vector.
  //   (propagated)     — whatever the embedder or searcher surfaced.
  absl::StatusOr<aegis::v1::PrompterHint>
  Retrieve(std::string_view transcript_text);

  Retriever(const Retriever &) = delete;
  Retriever &operator=(const Retriever &) = delete;
  Retriever(Retriever &&) = delete;
  Retriever &operator=(Retriever &&) = delete;

private:
  inference::Embedder *embedder_;      // not owned
  vectordb::VectorSearcher *searcher_; // not owned
  std::string collection_;
  Config config_;
  std::uint64_t next_hint_id_ = 1;

  // Dedupe state: the Qdrant point id of the top result last emitted
  // as a hint. Consecutive Retrieve() calls whose top match is the
  // SAME point id return NotFound — a speaker staying on the same
  // topic should not flood the viewer with the same corpus chunk
  // every window. Cleared back to the new top whenever a different
  // point wins, so A → B → A is three distinct emissions (topic
  // changed away and back).
  std::string last_top_point_id_;
};

} // namespace aegis::rag

#endif // AEGIS_ENGINE_CPP_SRC_RAG_RETRIEVER_H_
