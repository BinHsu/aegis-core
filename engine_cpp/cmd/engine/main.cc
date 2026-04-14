// engine_cpp/cmd/engine/main.cc
//
// Aegis Core engine process entrypoint. Phase 1 Session 3: minimal
// gRPC server that accepts connections and responds to Health. Real
// whisper.cpp inference wiring lands in Session 4.

#include <csignal>
#include <cstdlib>
#include <iostream>
#include <memory>
#include <string>

#include "grpcpp/grpcpp.h"

#include "engine_cpp/src/grpc/aegis_engine_service.h"
#include "engine_cpp/src/inference/whisper_engine.h"
#include "engine_cpp/src/session/resource_budget.h"

namespace {

// Default budget: 5.5 GB — ADR-0010 Local mode capacity (16 GB ceiling
// minus 8 GB for OS/browser/frontend/Go GW minus 2.5 GB fixed models).
// Overridable at runtime via --budget_mb=<N>; Phase 2 will take this
// from ResourceBudget config or /models/manifest.json.
constexpr std::size_t kDefaultBudgetBytes = 5'500ULL * 1024 * 1024;

// Shutdown handling — Phase 4 graceful drain is in ADR-0006 §Graceful
// Shutdown. Session 3 just exits cleanly on SIGINT/SIGTERM.
std::unique_ptr<::grpc::Server> g_server;

void HandleShutdown(int /*signum*/) {
  if (g_server) {
    g_server->Shutdown();
  }
}

} // namespace

int main(int /*argc*/, char ** /*argv*/) {
  std::signal(SIGINT, HandleShutdown);
  std::signal(SIGTERM, HandleShutdown);

  const std::string address = "0.0.0.0:50051";

  // Model path — AEGIS_MODEL_PATH env var, else a sane default relative
  // to CWD. Phase 2+ will switch to absl::flags and/or a config file.
  std::string model_path = "models/ggml-tiny.en.bin";
  if (const char *env = std::getenv("AEGIS_MODEL_PATH"); env != nullptr) {
    model_path = env;
  }

  aegis::session::ResourceBudget budget(kDefaultBudgetBytes);
  aegis::grpc_service::AegisEngineServiceImpl service(&budget, model_path);

  ::grpc::ServerBuilder builder;
  // Session 3: insecure for local dev. Production uses mTLS via Istio
  // service mesh (ARCH §8 Zero Trust Networking) — that is an Istio /
  // aegis-aws-landing-zone concern, not a server-side cert config.
  builder.AddListeningPort(address, ::grpc::InsecureServerCredentials());
  builder.RegisterService(&service);

  g_server = builder.BuildAndStart();
  if (!g_server) {
    std::cerr << "aegis-engine: failed to start gRPC server on " << address
              << std::endl;
    return EXIT_FAILURE;
  }

  std::cout << "aegis-engine: listening on " << address << std::endl;
  std::cout << "  budget_total_bytes=" << budget.TotalBytes() << std::endl;
  std::cout << "  model_path=" << model_path << std::endl;
  std::cout << "  version=0.1.0-phase1-s4d" << std::endl;
  std::cout << "  whisper: " << aegis::inference::WhisperSystemInfo()
            << std::endl;

#ifdef AEGIS_DEV_AUDIO_DUMP
  // ADR-0005 R7: this banner is intentional — it is the audit signal
  // that a dev-only build reached a human. Production images MUST NOT
  // contain this symbol (CI grep check enforces this per ROADMAP 4b).
  std::cerr << "aegis-engine: ⚠️  AEGIS_DEV_AUDIO_DUMP is enabled — "
               "this is a DEBUG build. Do not use in production."
            << std::endl;
#endif

  g_server->Wait();
  return EXIT_SUCCESS;
}
