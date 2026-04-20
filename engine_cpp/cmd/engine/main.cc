// engine_cpp/cmd/engine/main.cc
//
// Aegis Core engine process entrypoint. Two modes:
//
//   engine              → start the gRPC server (default)
//   engine seed ...     → run the RAG seed subcommand (Phase 3b Slice 6)
//
// Future subcommands (Phase 4+ ideas: migrate, repair-index, validate)
// slot in with the same dispatch pattern — strip argv[1] and hand off
// to the subcommand's entry point.
//
// Server-mode startup order per ADR-0010 §Revision (2026-04-15):
//   1. Load all models (each registers with ModelBudget)
//   2. Compute session pool = pod_limit - ModelBudget::TotalUsedBytes()
//   3. Construct SessionBudget with the session pool
//   4. Start gRPC server

#include <csignal>
#include <cstdlib>
#include <cstring>
#include <iostream>
#include <memory>
#include <string>
#include <string_view>

#include "grpcpp/grpcpp.h"
#include "prometheus/exposer.h"

#include "engine_cpp/cmd/engine/seed.h"
#include "engine_cpp/src/grpc/aegis_engine_service.h"
#include "engine_cpp/src/inference/whisper_engine.h"
#include "engine_cpp/src/metrics/metrics.h"
#include "engine_cpp/src/models/manifest_loader.h"
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

int main(int argc, char **argv) {
  // Subcommand dispatch: if argv[1] is a known subcommand, strip it
  // and hand off. absl::flags (used by seed) expects argv[0] to be
  // the binary name — passing argv+1 effectively makes the subcommand
  // name the new argv[0], which absl's parser ignores, so the flag
  // parsing inside the subcommand sees a clean set of tokens.
  if (argc >= 2 && std::string_view(argv[1]) == "seed") {
    return aegis::engine_cmd::RunSeed(argc - 1, argv + 1);
  }

  std::signal(SIGINT, HandleShutdown);
  std::signal(SIGTERM, HandleShutdown);

  const std::string address = "0.0.0.0:50051";

  // Manifest + model-root — ADR-0026 CAS layout.
  //   AEGIS_MANIFEST_PATH: path to models/manifest.json (bundled in OCI
  //                        image in Cloud; under $REPO_ROOT/models/ for LAN)
  //   AEGIS_MODEL_PATH:    root directory under which the CAS layout lives
  //                        (<root>/<id>/<sha>.<ext>)
  // LAN's app_local supervisor sets both to absolute paths derived from
  // BUILD_WORKSPACE_DIRECTORY. Cloud's Deployment manifest sets them from
  // the image contents (manifest) + S3 Files mount (model root).
  std::string manifest_path = "models/manifest.json";
  if (const char *env = std::getenv("AEGIS_MANIFEST_PATH"); env != nullptr) {
    manifest_path = env;
  }
  std::string model_root = "models";
  if (const char *env = std::getenv("AEGIS_MODEL_PATH"); env != nullptr) {
    model_root = env;
  }

  // Pod memory limit — AEGIS_POD_MEMORY_LIMIT env var (bytes), else
  // default 8 GB. Cloud deployments set this from K8s resource limits.
  std::size_t pod_limit = kDefaultPodMemoryLimit;
  if (const char *env = std::getenv("AEGIS_POD_MEMORY_LIMIT"); env != nullptr) {
    pod_limit = std::strtoull(env, nullptr, 10);
  }

  // -------------------------------------------------------------------
  // CAS preflight walker (ADR-0026 §Engine responsibilities).
  // Walk every required=true manifest entry; stat + size + SHA-256 verify
  // each against its CAS path under model_root. Fail-fast with operator-
  // readable diagnostic on any miss / mismatch — this is the signal that
  // operators should re-run the CI populator / download_models.sh, not an
  // invitation for engine self-recovery (explicitly rejected per
  // ADR-0026 §"Pruning — prohibited" revision 2026-04-20 reasoning).
  // -------------------------------------------------------------------
  auto manifest_or = aegis::models::LoadManifest(manifest_path);
  if (!manifest_or.ok()) {
    std::cerr << "aegis-engine: FATAL — cannot load manifest from `"
              << manifest_path << "`: " << manifest_or.status() << std::endl;
    return EXIT_FAILURE;
  }
  const aegis::models::Manifest &manifest = *manifest_or;

  if (absl::Status s = aegis::models::VerifyAllRequired(model_root, manifest);
      !s.ok()) {
    std::cerr << "aegis-engine: FATAL — CAS preflight failed.\n"
              << s.message() << std::endl;
    return EXIT_FAILURE;
  }

  // Resolve the whisper transcription model: the single required=true
  // entry whose type is "transcription". Walker already verified it,
  // so existence + size + SHA are known good.
  const aegis::models::ModelEntry *whisper_entry = nullptr;
  for (const auto &e : manifest.models) {
    if (e.required && e.type == "transcription") {
      if (whisper_entry != nullptr) {
        std::cerr << "aegis-engine: FATAL — multiple required=true "
                     "transcription entries in manifest (`"
                  << whisper_entry->id << "`, `" << e.id
                  << "`); pick one per manifest honesty discipline."
                  << std::endl;
        return EXIT_FAILURE;
      }
      whisper_entry = &e;
    }
  }
  if (whisper_entry == nullptr) {
    std::cerr << "aegis-engine: FATAL — no required=true manifest entry of "
                 "type=\"transcription\"; cannot pick a model for "
                 "WhisperEngine."
              << std::endl;
    return EXIT_FAILURE;
  }
  const std::string model_path =
      aegis::models::ResolveCasPath(model_root, *whisper_entry);

  // Step 1: Register the whisper model with ModelBudget using the
  // manifest's size (in-memory footprint ≈ on-disk size for q4-quantized
  // ggml files). Precise estimated_ram_bytes is in the manifest but not
  // surfaced via ModelEntry today — size_bytes is a close-enough proxy.
  aegis::session::ModelBudget::Register(
      whisper_entry->id, static_cast<std::size_t>(whisper_entry->size_bytes));

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
  // Insecure for dev + staging. CLOUD mode mTLS is NOT mesh-provided
  // per ADR-0031 — it arrives via cert-manager Certificate CRs +
  // `grpc::experimental::TlsServerCredentials` with
  // `FileWatcherCertificateProvider` in C-2. LOCAL mode keeps
  // plaintext by design (ADR-0031 §"LOCAL mode posture"): parent/
  // child processes share the host trust domain.
  builder.AddListeningPort(address, ::grpc::InsecureServerCredentials());
  builder.RegisterService(&service);

  // Keepalive policy — MUST accept the gateway client's 30s PING cadence
  // (see `gateway_go/cmd/gateway/main.go` keepaliveTime per ADR-0006).
  // gRPC C++ server defaults reject pings more frequent than 5 minutes
  // and disallow pings without active calls; under default the gateway's
  // 30s PINGs trigger ENHANCE_YOUR_CALM GoAway + reconnect every ~4 min,
  // causing WhisperEngine::Create to reload the model from scratch every
  // cycle and transcript segments never complete (observed 2026-04-20 in
  // LAN smoke). Accept PING every 20s even without active calls.
  builder.AddChannelArgument(
      GRPC_ARG_HTTP2_MIN_RECV_PING_INTERVAL_WITHOUT_DATA_MS, 20000);
  builder.AddChannelArgument(GRPC_ARG_KEEPALIVE_PERMIT_WITHOUT_CALLS, 1);

  g_server = builder.BuildAndStart();
  if (!g_server) {
    std::cerr << "aegis-engine: failed to start gRPC server on " << address
              << std::endl;
    return EXIT_FAILURE;
  }

  // C-Obs-1 — Prometheus pull exposer on :8081 by default.
  // Port per ldz #46 §"Q3" K8s controller-runtime convention
  // (`:8080` main + `:8081` mgmt/metrics companion). The engine
  // itself has no `:8080`; port chosen for cross-service symmetry
  // with the gateway.
  //
  // AEGIS_ENGINE_METRICS_ADDR overrides the default:
  //   unset       → `0.0.0.0:8081` (default, enabled)
  //   non-empty   → use the provided addr (e.g., `127.0.0.1:9999`)
  //   explicitly "" → skip Exposer construction entirely
  // LOCAL-mode `app_local` sets this to "" for the engine child so
  // it doesn't collide with gateway's own :8081 on the same host.
  std::string metrics_address = "0.0.0.0:8081";
  if (const char *env = std::getenv("AEGIS_ENGINE_METRICS_ADDR");
      env != nullptr) {
    metrics_address = env;
  }

  std::unique_ptr<prometheus::Exposer> exposer;
  if (!metrics_address.empty()) {
    exposer = std::make_unique<prometheus::Exposer>(metrics_address);
    exposer->RegisterCollectable(aegis::metrics::GlobalRegistry());
  }

  std::cout << "aegis-engine: listening on " << address << std::endl;
  if (exposer) {
    std::cout << "  metrics on " << metrics_address << "/metrics" << std::endl;
  } else {
    std::cout << "  metrics disabled (AEGIS_ENGINE_METRICS_ADDR set to empty)"
              << std::endl;
  }
  std::cout << "  pod_memory_limit=" << pod_limit << std::endl;
  std::cout << "  model_budget=" << model_total << std::endl;
  std::cout << "  session_pool=" << session_pool << std::endl;
  std::cout << "  model_path=" << model_path << std::endl;
  std::cout << "  version=0.2.0-phase3b-s3" << std::endl;
  std::cout << "  whisper: " << aegis::inference::WhisperSystemInfo()
            << std::endl;

  // Log model breakdown + publish per-model `aegis_engine_model_loaded`
  // gauges for Prometheus scrape.
  for (const auto &[name, bytes] : aegis::session::ModelBudget::Breakdown()) {
    std::cout << "  model: " << name << " = " << bytes << " bytes" << std::endl;
    aegis::metrics::ModelLoaded().Add({{"model", name}}).Set(1.0);
  }

  // Signal to scrapers that startup completed and the gRPC server is
  // past BuildAndStart(). Set exactly once here; clearing on shutdown
  // isn't load-bearing because pod termination stops the exposer too.
  aegis::metrics::Up().Add({}).Set(1.0);

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
