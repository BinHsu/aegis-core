// engine_cpp/src/grpc/aegis_engine_service.cc

#include "engine_cpp/src/grpc/aegis_engine_service.h"

#include <chrono>
#include <utility>

#include "engine_cpp/src/metrics/metrics.h"
#include "engine_cpp/src/session/session.h"
#include "engine_cpp/src/session/session_budget.h"

namespace aegis::grpc_service {
namespace {

// RAII helper — on construction captures start time; on destruction
// increments `aegis_engine_rpc_total{method, status}` and observes
// `aegis_engine_rpc_duration_seconds{method}`. The destructor runs
// after the handler returns, so `final_status` must be set by the
// handler before the scope exits.
class RpcInstrument {
public:
  explicit RpcInstrument(const char *method)
      : method_(method), start_(std::chrono::steady_clock::now()) {}

  void SetStatus(const ::grpc::Status &status) {
    // Coarse "ok" vs "error" labels for first cut; per-code
    // fan-out can refine later if operators want it without
    // expanding cardinality at handler-entry time.
    final_code_ = status.ok() ? "ok" : "error";
  }

  ~RpcInstrument() {
    const auto end = std::chrono::steady_clock::now();
    const double seconds = std::chrono::duration<double>(end - start_).count();
    metrics::RpcTotal()
        .Add({{"method", method_}, {"status", final_code_}})
        .Increment();
    metrics::RpcDurationSeconds()
        .Add({{"method", method_}}, metrics::RpcDurationBuckets())
        .Observe(seconds);
  }

private:
  const char *method_;
  std::chrono::steady_clock::time_point start_;
  std::string final_code_ = "UNKNOWN";
};

} // namespace

AegisEngineServiceImpl::AegisEngineServiceImpl(
    session::SessionBudget *budget, std::string model_path,
    inference::Embedder *embedder, vectordb::VectorSearcher *searcher) noexcept
    : budget_(budget), model_path_(std::move(model_path)), embedder_(embedder),
      searcher_(searcher) {}

::grpc::Status AegisEngineServiceImpl::StreamTranscribe(
    ::grpc::ServerContext * /*context*/,
    ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                               aegis::v1::IngestMessage> *stream) {
  RpcInstrument instrument("StreamTranscribe");

  // Per ADR-0010 Sub-decision 1: one Session per stream, lives on the
  // grpc-cpp sync thread's stack, runs top-to-bottom.
  session::Session s(budget_, model_path_, embedder_, searcher_);
  const absl::Status status = s.Run(stream);
  if (!status.ok()) {
    ::grpc::Status result(static_cast<::grpc::StatusCode>(status.code()),
                          std::string(status.message()));
    instrument.SetStatus(result);
    return result;
  }
  instrument.SetStatus(::grpc::Status::OK);
  return ::grpc::Status::OK;
}

::grpc::Status
AegisEngineServiceImpl::Health(::grpc::ServerContext * /*context*/,
                               const aegis::v1::HealthRequest * /*request*/,
                               aegis::v1::HealthResponse *response) {
  RpcInstrument instrument("Health");

  response->set_ready(true);

  auto *info = response->mutable_info();
  info->set_model(model_path_); // Phase 1: report configured model path
  info->set_backend("cpu");
  info->set_version("0.1.0-phase1-s4d");

  auto *status = response->mutable_status();
  status->set_budget_bytes_used(budget_->BytesUsed());
  status->set_budget_bytes_total(budget_->TotalBytes());
  status->set_active_sessions(0);

  instrument.SetStatus(::grpc::Status::OK);
  return ::grpc::Status::OK;
}

} // namespace aegis::grpc_service
