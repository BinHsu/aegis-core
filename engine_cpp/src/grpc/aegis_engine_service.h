// engine_cpp/src/grpc/aegis_engine_service.h
//
// Implementation of aegis.v1.Engine gRPC service. Session 4d wires
// StreamTranscribe to the Session state machine (see
// engine_cpp/src/session/session.h); Health continues to report
// budget + version. Per ADR-0010 Sub-decision 1, each StreamTranscribe
// invocation runs on its own grpc-cpp sync thread and owns a Session
// object for the stream's lifetime.

#ifndef AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_
#define AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_

#include <string>

#include "grpcpp/grpcpp.h"
#include "proto/aegis/v1/aegis.grpc.pb.h"

namespace aegis::session {
class SessionBudget;
} // namespace aegis::session

namespace aegis::grpc_service {

class AegisEngineServiceImpl final : public aegis::v1::Engine::Service {
public:
  // `budget` and `model_path` must outlive this service instance.
  AegisEngineServiceImpl(session::SessionBudget *budget,
                         std::string model_path) noexcept;

  ::grpc::Status StreamTranscribe(
      ::grpc::ServerContext *context,
      ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                                 aegis::v1::IngestMessage> *stream) override;

  ::grpc::Status Health(::grpc::ServerContext *context,
                        const aegis::v1::HealthRequest *request,
                        aegis::v1::HealthResponse *response) override;

private:
  session::SessionBudget *budget_; // not owned
  std::string model_path_;
};

} // namespace aegis::grpc_service

#endif // AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_
