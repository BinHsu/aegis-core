// engine_cpp/src/grpc/aegis_engine_service.cc

#include "engine_cpp/src/grpc/aegis_engine_service.h"

#include <algorithm>
#include <chrono>
#include <string>
#include <utility>
#include <vector>

#include "absl/strings/match.h"
#include "absl/strings/str_cat.h"
#include "absl/strings/str_replace.h"
#include "engine_cpp/src/metrics/metrics.h"
#include "engine_cpp/src/session/session.h"
#include "engine_cpp/src/session/session_budget.h"
#include "engine_cpp/src/vectordb/qdrant_client.h"

namespace aegis::grpc_service {
namespace {

// RAII helper — on construction captures start time; on destruction
// increments `aegis_core_engine_rpc_total{method, status}` and observes
// `aegis_core_engine_rpc_duration_seconds{method}`. The destructor runs
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
    inference::Embedder *embedder, vectordb::VectorSearcher *searcher,
    vectordb::CollectionLister *lister) noexcept
    : budget_(budget), model_path_(std::move(model_path)), embedder_(embedder),
      searcher_(searcher), lister_(lister) {}

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

::grpc::Status AegisEngineServiceImpl::ListCorpora(
    ::grpc::ServerContext * /*context*/,
    const aegis::v1::EngineListCorporaRequest *request,
    aegis::v1::EngineListCorporaResponse *response) {
  RpcInstrument instrument("ListCorpora");

  const std::string &tenant_id = request->tenant_id();
  if (tenant_id.empty()) {
    const ::grpc::Status result(::grpc::StatusCode::INVALID_ARGUMENT,
                                "ListCorpora: tenant_id is required");
    instrument.SetStatus(result);
    return result;
  }

  if (lister_ == nullptr) {
    // Qdrant was never configured at engine startup — LAN demo
    // without QDRANT_URL or cloud pod without the secret mount.
    // Return UNAVAILABLE; the gateway surfaces this so the Host UI
    // can fall back to a hardcoded list without crashing.
    const ::grpc::Status result(
        ::grpc::StatusCode::UNAVAILABLE,
        "ListCorpora: engine has no Qdrant client (RAG disabled)");
    instrument.SetStatus(result);
    return result;
  }

  auto names_or = lister_->ListCollections();
  if (!names_or.ok()) {
    const ::grpc::Status result(
        static_cast<::grpc::StatusCode>(names_or.status().code()),
        std::string(names_or.status().message()));
    instrument.SetStatus(result);
    return result;
  }

  // Tenant isolation per ADR-0022 §Decision — collection names are
  // `aegis_<tenant>_<corpus>`. Filter by prefix so collections
  // belonging to other tenants in the same Qdrant instance never
  // reach the caller. The Qdrant list endpoint has no server-side
  // filter; client-side prefix check is the enforcement point (see
  // PR discussion `Qdrant 的 ListCollections 不能下 tenant filter`).
  const std::string prefix = absl::StrCat("aegis_", tenant_id, "_");

  // Names returned in whatever order Qdrant chose — sort for
  // determinism so the UI dropdown is stable across calls.
  std::vector<std::string> matched;
  for (const auto &name : *names_or) {
    if (absl::StartsWith(name, prefix)) {
      matched.push_back(name);
    }
  }
  std::sort(matched.begin(), matched.end());

  for (const auto &name : matched) {
    auto *info = response->add_corpora();
    info->set_id(name);
    // Label = name with prefix stripped + underscores → spaces, so
    // "aegis_demo_taiwan" renders as "taiwan". The UI presents the
    // label; the id is the rag_id wire value on CreateMeeting.
    const std::string stem = name.substr(prefix.size());
    info->set_label(absl::StrReplaceAll(stem, {{"_", " "}}));
  }

  instrument.SetStatus(::grpc::Status::OK);
  return ::grpc::Status::OK;
}

} // namespace aegis::grpc_service
