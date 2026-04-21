// engine_cpp/src/session/session.h
//
// A Session owns the lifetime of one StreamTranscribe gRPC stream.
// Per ADR-0010 Sub-decision 1, Aegis's Phase 1 threading model is
// 1-session-1-thread — the grpc-cpp sync server dispatches each
// bidi stream onto its own thread, and the Session object lives on
// that thread's stack. When the stream closes, ~Session runs, which
// releases the SessionBudget reservation and tears down the
// WhisperEngine. No shared state, no reference counting.
//
// The state machine is driven from the IngestMessage oneof per
// ADR-0006:
//
//   [Waiting for SessionStart]
//     - SessionStart          → transition to Active; Reserve budget;
//                               WhisperEngine::Create
//     - anything else         → INVALID_ARGUMENT; return
//
//   [Active]
//     - PcmChunk              → append int16 LE bytes to ring buffer
//                               as normalized floats; emit nothing
//                               (batched transcription happens on
//                               END_STREAM for Phase 1; incremental
//                               emission is Session 5+)
//     - OpusChunk             → lazy-init an OpusDecoder on first
//                               packet, decode to 16 kHz mono float
//                               PCM, append to ring buffer. Per
//                               ADR-0016, codec work lives here, not
//                               in the gateway. A single corrupt
//                               frame is logged-and-dropped, not
//                               fatal to the session.
//     - ControlEvent PAUSE    → transition to Paused
//     - ControlEvent RESUME   → no-op (already active)
//     - ControlEvent END      → flush + emit TranscriptSegment(s)
//                               and return OK
//     - SessionStart          → INVALID_ARGUMENT (duplicate)
//
//   [Paused]
//     - PcmChunk / OpusChunk  → drop (host's audio is frozen per
//                               ADR-0006 WebRTC Disconnected state)
//     - ControlEvent RESUME   → transition to Active
//     - ControlEvent END      → flush what we have; return OK
//     - ControlEvent PAUSE    → no-op
//     - SessionStart          → INVALID_ARGUMENT
//
// Session is non-copyable / non-movable — it is tightly bound to a
// single stream and thread.

#ifndef AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_H_
#define AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_H_

#include <cstddef>
#include <string>

#include "absl/status/status.h"
#include "grpcpp/grpcpp.h"
#include "proto/aegis/v1/aegis.grpc.pb.h"

namespace aegis::inference {
class Embedder;
} // namespace aegis::inference

namespace aegis::vectordb {
class VectorSearcher;
} // namespace aegis::vectordb

namespace aegis::session {

class SessionBudget;

class Session {
public:
  // `budget` and `model_path` must outlive the Session. `embedder` and
  // `searcher` are OPTIONAL process-scoped handles for RAG retrieval;
  // nullptr on either skips the hint-emission path entirely (transcript
  // still flows). RAG is additionally gated per-session by
  // `SessionStart.rag_id` (empty string = no retrieval even when both
  // services are available). See Phase 3b ROADMAP.
  Session(SessionBudget *budget, const std::string &model_path,
          inference::Embedder *embedder = nullptr,
          vectordb::VectorSearcher *searcher = nullptr) noexcept;
  ~Session() = default;

  // Drive the state machine to completion. Returns absl::OkStatus on
  // clean termination, or a status that the gRPC handler converts to
  // the wire. Does not throw.
  absl::Status
  Run(::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                                 aegis::v1::IngestMessage> *stream);

  Session(const Session &) = delete;
  Session &operator=(const Session &) = delete;
  Session(Session &&) = delete;
  Session &operator=(Session &&) = delete;

private:
  SessionBudget *budget_; // not owned
  std::string model_path_;
  inference::Embedder *embedder_;      // not owned, may be nullptr
  vectordb::VectorSearcher *searcher_; // not owned, may be nullptr
};

} // namespace aegis::session

#endif // AEGIS_ENGINE_CPP_SRC_SESSION_SESSION_H_
