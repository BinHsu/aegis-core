// engine_cpp/src/metrics/metrics.cc

#include "engine_cpp/src/metrics/metrics.h"

#include "prometheus/counter.h"
#include "prometheus/family.h"
#include "prometheus/gauge.h"
#include "prometheus/histogram.h"
#include "prometheus/registry.h"

namespace aegis::metrics {

std::shared_ptr<prometheus::Registry> GlobalRegistry() {
  static auto registry = std::make_shared<prometheus::Registry>();
  return registry;
}

prometheus::Family<prometheus::Gauge> &Up() {
  static auto &family = prometheus::BuildGauge()
                            .Name("aegis_engine_up")
                            .Help("1 when the engine process is up and the "
                                  "gRPC server has started listening.")
                            .Register(*GlobalRegistry());
  return family;
}

prometheus::Family<prometheus::Gauge> &ModelLoaded() {
  static auto &family = prometheus::BuildGauge()
                            .Name("aegis_engine_model_loaded")
                            .Help("1 when the named model is registered with "
                                  "the ModelBudget (i.e. available for "
                                  "session use), 0 otherwise.")
                            .Register(*GlobalRegistry());
  return family;
}

prometheus::Family<prometheus::Counter> &RpcTotal() {
  static auto &family = prometheus::BuildCounter()
                            .Name("aegis_engine_rpc_total")
                            .Help("Total RPCs received by the engine, "
                                  "labelled by method and terminal status.")
                            .Register(*GlobalRegistry());
  return family;
}

prometheus::Family<prometheus::Histogram> &RpcDurationSeconds() {
  static auto &family =
      prometheus::BuildHistogram()
          .Name("aegis_engine_rpc_duration_seconds")
          .Help("Handler duration in seconds, labelled by method. "
                "Buckets span unary (Health, ~ms) + streaming "
                "(StreamTranscribe, minutes) in one metric.")
          .Register(*GlobalRegistry());
  return family;
}

const prometheus::Histogram::BucketBoundaries &RpcDurationBuckets() {
  // 1ms / 10ms / 100ms / 1s / 5s / 30s / 2min / 10min.
  // Chosen to give at least two buckets of resolution at both the
  // unary (Health: sub-10ms common) and streaming (StreamTranscribe:
  // tens of seconds common) ends without exploding bucket count.
  static const prometheus::Histogram::BucketBoundaries buckets = {
      0.001, 0.01, 0.1, 1.0, 5.0, 30.0, 120.0, 600.0,
  };
  return buckets;
}

} // namespace aegis::metrics
