// engine_cpp/src/grpc/aegis_engine_service.h
//
// Implementation of aegis.v1.Engine gRPC service. Session 3 skeleton
// returns UNIMPLEMENTED for StreamTranscribe and a minimal Health
// response. Full wiring (whisper inference, PcmChunk handling,
// ControlEvent state machine) lands in later sessions per
// ADR-0010 Sub-decision 1 (1-session-1-thread model).

#ifndef AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_
#define AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_

#include "grpcpp/grpcpp.h"
#include "proto/aegis/v1/aegis.grpc.pb.h"

namespace aegis::session {
class ResourceBudget;
}  // namespace aegis::session

namespace aegis::grpc_service {

class AegisEngineServiceImpl final : public aegis::v1::Engine::Service {
 public:
  // `budget` is not owned; must outlive this service instance.
  explicit AegisEngineServiceImpl(session::ResourceBudget* budget) noexcept;

  ::grpc::Status StreamTranscribe(
      ::grpc::ServerContext* context,
      ::grpc::ServerReaderWriter<aegis::v1::EgressMessage,
                                 aegis::v1::IngestMessage>* stream) override;

  ::grpc::Status Health(::grpc::ServerContext* context,
                        const aegis::v1::HealthRequest* request,
                        aegis::v1::HealthResponse* response) override;

 private:
  session::ResourceBudget* budget_;  // not owned
};

}  // namespace aegis::grpc_service

#endif  // AEGIS_ENGINE_CPP_SRC_GRPC_AEGIS_ENGINE_SERVICE_H_
