// engine_cpp/src/grpc/aegis_engine_service.cc

#include "engine_cpp/src/grpc/aegis_engine_service.h"

#include <utility>

#include "engine_cpp/src/session/resource_budget.h"
#include "engine_cpp/src/session/session.h"

namespace aegis::grpc_service {

AegisEngineServiceImpl::AegisEngineServiceImpl(session::ResourceBudget *budget,
                                               std::string model_path) noexcept
    : budget_(budget), model_path_(std::move(model_path)) {}

::grpc::Status AegisEngineServiceImpl::StreamTranscribe(
    ::grpc::ServerContext * /*context*/,
    ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                               aegis::v1::IngestMessage> *stream) {
  // Per ADR-0010 Sub-decision 1: one Session per stream, lives on the
  // grpc-cpp sync thread's stack, runs top-to-bottom.
  session::Session s(budget_, model_path_);
  const absl::Status status = s.Run(stream);
  if (!status.ok()) {
    return ::grpc::Status(static_cast<::grpc::StatusCode>(status.code()),
                          std::string(status.message()));
  }
  return ::grpc::Status::OK;
}

::grpc::Status
AegisEngineServiceImpl::Health(::grpc::ServerContext * /*context*/,
                               const aegis::v1::HealthRequest * /*request*/,
                               aegis::v1::HealthResponse *response) {
  response->set_ready(true);

  auto *info = response->mutable_info();
  info->set_model(model_path_); // Phase 1: report configured model path
  info->set_backend("cpu");
  info->set_version("0.1.0-phase1-s4d");

  auto *status = response->mutable_status();
  status->set_budget_bytes_used(budget_->BytesUsed());
  status->set_budget_bytes_total(budget_->TotalBytes());
  status->set_active_sessions(0);

  return ::grpc::Status::OK;
}

} // namespace aegis::grpc_service
