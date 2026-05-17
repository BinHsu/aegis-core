// engine_cpp/tests/unit/metrics_test.cc
//
// The metric NAME is the load-bearing boundary of the engine's
// observability surface: the landing-zone Prometheus scrape, every
// Grafana panel, and every PrometheusRule alert key off the exact
// string. CLAUDE.md Rule 11 requires every metric family this repo
// owns to carry the full `aegis_core_engine_` prefix — a bare
// `aegis_engine_` prefix is a rule violation.
//
// These tests pin the exact family names produced by the metrics
// registry so a regression back to the bare prefix (or any typo)
// fails the build rather than silently breaking the dashboards
// downstream. They are written to FAIL on the pre-rename code
// (`aegis_engine_*`) and PASS only once every family carries the
// full `aegis_core_engine_` prefix.
//
// Why each family is touched with `.Add({})` first: prometheus-cpp's
// `Registry::Collect()` only emits a family that has at least one
// child metric. A freshly registered `Family<T>` with no `.Add(...)`
// call is collected as nothing. The family *name* is fixed at
// `BuildX().Name(...)` registration time regardless of children, so
// to read the name back through the collection API the test must
// first materialise a child. `.Add({})` (empty label set) is the
// minimal, valid way to do that for every metric type.

#include "engine_cpp/src/metrics/metrics.h"

#include <algorithm>
#include <set>
#include <string>
#include <vector>

#include "prometheus/registry.h"
#include "gtest/gtest.h"

namespace {

// Touch every metric family — registering it AND materialising one
// child so `Registry::Collect()` surfaces the family — then return the
// sorted set of family names the global registry exposes.
std::vector<std::string> GatheredNames() {
  // Force lazy registration of all four families and give each a
  // child so Collect() emits it.
  aegis::metrics::Up().Add({});
  aegis::metrics::ModelLoaded().Add({{"model", "probe"}});
  aegis::metrics::RpcTotal().Add({{"method", "Probe"}, {"status", "ok"}});
  aegis::metrics::RpcDurationSeconds().Add(
      {{"method", "Probe"}}, aegis::metrics::RpcDurationBuckets());

  std::vector<std::string> names;
  for (const auto &mf : aegis::metrics::GlobalRegistry()->Collect()) {
    names.push_back(mf.name);
  }
  std::sort(names.begin(), names.end());
  return names;
}

// TestMetricNames_FullRepoPrefix pins every registered metric family
// name to the `aegis_core_engine_` prefix mandated by CLAUDE.md
// Rule 11. Regression guard for the bare-`aegis_engine_` →
// `aegis_core_engine_` rename.
TEST(EngineMetricNames, FullRepoPrefix) {
  const std::set<std::string> want = {
      "aegis_core_engine_up",
      "aegis_core_engine_model_loaded",
      "aegis_core_engine_rpc_total",
      "aegis_core_engine_rpc_duration_seconds",
  };

  const std::vector<std::string> got = GatheredNames();
  const std::set<std::string> got_set(got.begin(), got.end());

  EXPECT_EQ(got_set, want)
      << "engine metric family names must exactly match the "
         "aegis_core_engine_ prefixed set (CLAUDE.md Rule 11)";
}

// TestMetricNames_NoBarePrefix is the negative boundary: no registered
// family may carry the bare `aegis_engine_` prefix. A metric named
// exactly `aegis_core_engine_...` correctly contains the substring
// `engine_` but NOT the standalone bare prefix `aegis_engine_`.
TEST(EngineMetricNames, NoBarePrefix) {
  const std::string bare_prefix = "aegis_engine_";
  const std::vector<std::string> names = GatheredNames();
  ASSERT_FALSE(names.empty())
      << "no metric families collected — the test setup failed to "
         "register/materialise the families";
  for (const std::string &name : names) {
    EXPECT_FALSE(name.rfind(bare_prefix, 0) == 0)
        << "metric \"" << name << "\" uses the bare \"" << bare_prefix
        << "\" prefix; CLAUDE.md Rule 11 requires the full "
           "\"aegis_core_engine_\" prefix";
  }
}

} // namespace
