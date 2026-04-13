// engine_cpp/src/grpc/aegis_engine_service.cc

#include "engine_cpp/src/grpc/aegis_engine_service.h"

#include "engine_cpp/src/session/resource_budget.h"

namespace aegis::grpc_service {

AegisEngineServiceImpl::AegisEngineServiceImpl(
    session::ResourceBudget *budget) noexcept
    : budget_(budget) {}

::grpc::Status AegisEngineServiceImpl::StreamTranscribe(
    ::grpc::ServerContext * /*context*/,
    ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                               aegis::v1::IngestMessage> * /*stream*/) {
  // Session 3: skeleton only. Real implementation lands in Session 4
  // (whisper.cpp wiring) per ADR-0010 Sub-decision 1 (1-session-1-thread).
  return ::grpc::Status(::grpc::StatusCode::UNIMPLEMENTED,
                        "StreamTranscribe not yet implemented (Phase 1 S4)");
}

::grpc::Status
AegisEngineServiceImpl::Health(::grpc::ServerContext * /*context*/,
                               const aegis::v1::HealthRequest * /*request*/,
                               aegis::v1::HealthResponse *response) {
  response->set_ready(true);

  auto *info = response->mutable_info();
  info->set_model("whisper-large-v3-turbo-q4");
  info->set_backend("cpu");
  info->set_version("0.1.0-phase1-s3");

  auto *status = response->mutable_status();
  status->set_budget_bytes_used(budget_->BytesUsed());
  status->set_budget_bytes_total(budget_->TotalBytes());
  status->set_active_sessions(0);

  return ::grpc::Status::OK;
}

} // namespace aegis::grpc_service
