package metrics

import (
	"regexp"
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The metric NAME is the load-bearing boundary of this package: a
// Grafana dashboard, a PrometheusRule alert, and the landing-zone
// scrape pipeline all key off the exact string. CLAUDE.md Rule 11
// requires every metric family this repo owns to carry the full
// `aegis_core_gateway_` prefix — a bare `aegis_gateway_` prefix is a
// rule violation. These tests pin the exact registered names so a
// regression back to the bare prefix (or any typo) fails the build
// rather than silently breaking the dashboards downstream.
//
// Why Describe() and not Gather(): a CounterVec / HistogramVec with
// no label-set populated, and a Counter never incremented, emit zero
// samples — prometheus.Registry.Gather() omits a family until it has
// at least one observation. The metric *name* is fixed at
// construction regardless of observations, so the name assertion must
// read it from the collector's Desc (via Describe), not from gathered
// samples. Describe() reports every metric's Desc unconditionally.

// wantMetricNames is the exact set of metric families the gateway
// registers. Keep this in lockstep with the package's exported vars
// and the init() MustRegister call.
var wantMetricNames = []string{
	"aegis_core_gateway_up",
	"aegis_core_gateway_active_sessions",
	"aegis_core_gateway_rpc_total",
	"aegis_core_gateway_rpc_duration_seconds",
	"aegis_core_gateway_hints_emitted_total",
	"aegis_core_gateway_host_transient_loss_total",
}

// allCollectors is the exact set of collectors init() registers.
func allCollectors() []prometheus.Collector {
	return []prometheus.Collector{
		Up, ActiveSessions, RpcTotal, RpcDurationSeconds,
		HintsEmittedTotal, HostTransientLossTotal,
	}
}

// descFqName extracts the fully-qualified metric name from a
// *prometheus.Desc. Desc.String() has the stable form
//
//	Desc{fqName: "aegis_core_gateway_up", help: "...", ...}
//
// so the fqName is the first quoted token.
var descFqNameRe = regexp.MustCompile(`fqName: "([^"]+)"`)

func descFqName(t *testing.T, d *prometheus.Desc) string {
	t.Helper()
	m := descFqNameRe.FindStringSubmatch(d.String())
	if m == nil {
		t.Fatalf("could not parse fqName from Desc: %s", d.String())
	}
	return m[1]
}

// describedNames returns the sorted set of metric family names the
// given collectors report via Describe — independent of whether any
// observation has been recorded.
func describedNames(t *testing.T, collectors []prometheus.Collector) []string {
	t.Helper()
	var names []string
	for _, c := range collectors {
		ch := make(chan *prometheus.Desc, 16)
		go func(c prometheus.Collector) {
			c.Describe(ch)
			close(ch)
		}(c)
		for d := range ch {
			names = append(names, descFqName(t, d))
		}
	}
	sort.Strings(names)
	return names
}

// TestMetricNames_FullRepoPrefix pins every registered metric family
// name to the `aegis_core_gateway_` prefix mandated by CLAUDE.md
// Rule 11. This is the regression guard for the bare-`aegis_gateway_`
// → `aegis_core_gateway_` rename: it fails loudly on the pre-rename
// code and passes only once every family carries the full prefix.
func TestMetricNames_FullRepoPrefix(t *testing.T) {
	got := describedNames(t, allCollectors())

	want := append([]string(nil), wantMetricNames...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("described metric count: got %d (%v), want %d (%v)",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("metric[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestMetricNames_NoBarePrefix is the negative boundary: no registered
// family may carry the bare `aegis_gateway_` prefix. A metric named
// exactly `aegis_core_gateway_...` correctly contains the substring
// `gateway_` but NOT the standalone bare prefix `aegis_gateway_`.
func TestMetricNames_NoBarePrefix(t *testing.T) {
	const barePrefix = "aegis_gateway_"
	for _, name := range describedNames(t, allCollectors()) {
		if len(name) >= len(barePrefix) && name[:len(barePrefix)] == barePrefix {
			t.Errorf("metric %q uses the bare %q prefix; "+
				"CLAUDE.md Rule 11 requires the full %q prefix",
				name, barePrefix, "aegis_core_gateway_")
		}
	}
}

// TestMetricNames_ServedByPackageRegistry confirms the package's own
// process-global registry (the one Handler() exposes on /metrics)
// actually serves the renamed families once they carry observations.
// Each metric is touched once so a CounterVec / HistogramVec
// materialises a child and the family surfaces in Gather() — this is
// the path a real Prometheus scrape exercises.
func TestMetricNames_ServedByPackageRegistry(t *testing.T) {
	// Materialise one child / observation per family so Gather() emits
	// each. Up / ActiveSessions are plain Gauges (always emitted);
	// the Vecs and the bare Counter need a touch.
	Up.Set(1)
	ActiveSessions.Set(0)
	RpcTotal.WithLabelValues("Probe", "ok").Inc()
	RpcDurationSeconds.WithLabelValues("Probe").Observe(0.01)
	HintsEmittedTotal.WithLabelValues("retriever").Inc()
	HostTransientLossTotal.Inc()

	mfs, err := registry.Gather()
	if err != nil {
		t.Fatalf("registry.Gather: %v", err)
	}
	got := make([]string, 0, len(mfs))
	for _, mf := range mfs {
		got = append(got, mf.GetName())
	}
	sort.Strings(got)

	want := append([]string(nil), wantMetricNames...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("gathered metric count: got %d (%v), want %d (%v)",
			len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gathered metric[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}
