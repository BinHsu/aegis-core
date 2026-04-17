// engine_cpp/cmd/engine/main.cc
//
// Aegis Core engine process entrypoint. Phase 1 Session 3: minimal
// gRPC server that accepts connections and responds to Health. Real
// whisper.cpp inference wiring lands in Session 4.
//
// Startup order per ADR-0010 §Revision (2026-04-15):
//   1. Load all models (each registers with ModelBudget)
//   2. Compute session pool = pod_limit - ModelBudget::TotalUsedBytes()
//   3. Construct SessionBudget with the session pool
//   4. Start gRPC server

#include <csignal>
#include <cstdlib>
#include <iostream>
#include <memory>
#include <string>

#include "grpcpp/grpcpp.h"

#include "engine_cpp/src/grpc/aegis_engine_service.h"
#include "engine_cpp/src/inference/whisper_engine.h"
#include "engine_cpp/src/session/model_budget.h"
#include "engine_cpp/src/session/session_budget.h"

namespace {

// Default pod memory limit: 8 GB — ADR-0010 Local mode capacity
// (16 GB ceiling minus ~8 GB for OS/browser/frontend/Go GW).
// Overridable at runtime via AEGIS_POD_MEMORY_LIMIT env var (bytes).
constexpr std::size_t kDefaultPodMemoryLimit = 8ULL * 1024 * 1024 * 1024;

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

  // Pod memory limit — AEGIS_POD_MEMORY_LIMIT env var (bytes), else
  // default 8 GB. Cloud deployments set this from K8s resource limits.
  std::size_t pod_limit = kDefaultPodMemoryLimit;
  if (const char *env = std::getenv("AEGIS_POD_MEMORY_LIMIT"); env != nullptr) {
    pod_limit = std::strtoull(env, nullptr, 10);
  }

  // Step 1: Models register with ModelBudget as they load.
  // (WhisperEngine is created per-session in Session::Run, but its
  // model weight footprint is fixed and known at startup. Register
  // the static model size here; the per-session working memory is
  // what SessionBudget tracks.)
  //
  // Phase 3b will add: GGMLEmbedder::Create(bge_m3_path) which also
  // calls ModelBudget::Register("bge-m3-Q4_K_M", ~400 MB).
  //
  // For now, whisper-tiny.en is ~75 MB. We register a conservative
  // estimate; the precise value will come from manifest.json in Phase 2+.
  aegis::session::ModelBudget::Register("whisper-tiny.en", 75ULL * 1024 * 1024);

  // Step 2: Verify models fit, then size the session pool.
  const std::size_t model_total = aegis::session::ModelBudget::TotalUsedBytes();
  if (model_total >= pod_limit) {
    std::cerr << "aegis-engine: FATAL — models (" << model_total
              << " bytes) exceed pod memory limit (" << pod_limit
              << " bytes). Cannot start." << std::endl;
    return EXIT_FAILURE;
  }
  const std::size_t session_pool = pod_limit - model_total;

  // Step 3: Construct SessionBudget from the remaining pool.
  aegis::session::SessionBudget session_budget(session_pool);
  aegis::grpc_service::AegisEngineServiceImpl service(&session_budget,
                                                      model_path);

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
  std::cout << "  pod_memory_limit=" << pod_limit << std::endl;
  std::cout << "  model_budget=" << model_total << std::endl;
  std::cout << "  session_pool=" << session_pool << std::endl;
  std::cout << "  model_path=" << model_path << std::endl;
  std::cout << "  version=0.2.0-phase3b-s3" << std::endl;
  std::cout << "  whisper: " << aegis::inference::WhisperSystemInfo()
            << std::endl;

  // Log model breakdown for observability.
  for (const auto &[name, bytes] : aegis::session::ModelBudget::Breakdown()) {
    std::cout << "  model: " << name << " = " << bytes << " bytes" << std::endl;
  }

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
