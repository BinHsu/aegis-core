// engine_cpp/src/metrics/metrics.h
//
// Prometheus metrics registry for the aegis-core-engine process — ldz #46
// observability contract, C-Obs-1 slice. Exposes:
//
//   aegis_core_engine_up — Gauge, 1 when the process is up
//   aegis_core_engine_model_loaded — Gauge, per-model 0/1 load state
//   aegis_core_engine_rpc_total — Counter, RPC count by {method, status}
//   aegis_core_engine_rpc_duration_seconds — Histogram, duration by {method}
//
// The pull exposer in `engine_cpp/cmd/engine/main.cc` binds the
// registry returned by `GlobalRegistry()` to the HTTP server on
// port 8081 (K8s controller-runtime convention — landing-zone #46
// §"Q3"). Histogram buckets cover both unary calls (Health, ~ms) and
// streaming calls (StreamTranscribe, up to minutes) in one metric —
// the compromise buckets trade resolution at the extremes for
// single-metric simplicity, revisit if per-endpoint resolution
// becomes load-bearing.

#ifndef AEGIS_ENGINE_CPP_SRC_METRICS_METRICS_H_
#define AEGIS_ENGINE_CPP_SRC_METRICS_METRICS_H_

#include <memory>

#include "prometheus/counter.h"
#include "prometheus/family.h"
#include "prometheus/gauge.h"
#include "prometheus/histogram.h"
#include "prometheus/registry.h"

namespace aegis::metrics {

// Process-global Prometheus registry. The pull exposer binds against
// this; all metric publishers write to the same instance. Not thread-
// safe to call during destruction of static storage — callers must
// hold references obtained at program start, not re-lookup per RPC.
std::shared_ptr<prometheus::Registry> GlobalRegistry();

// Metric families — callers chain `.Add({...labels...})` to get a
// concrete metric instance bound to a specific label-set. The family
// objects are statically initialised on first access and live until
// process exit.

prometheus::Family<prometheus::Gauge> &Up();
prometheus::Family<prometheus::Gauge> &ModelLoaded();
prometheus::Family<prometheus::Counter> &RpcTotal();
prometheus::Family<prometheus::Histogram> &RpcDurationSeconds();

// Histogram bucket boundaries for RpcDurationSeconds — 1ms to 10min.
// Exported so the implementation + any tests use the same boundaries.
const prometheus::Histogram::BucketBoundaries &RpcDurationBuckets();

} // namespace aegis::metrics

#endif // AEGIS_ENGINE_CPP_SRC_METRICS_METRICS_H_
