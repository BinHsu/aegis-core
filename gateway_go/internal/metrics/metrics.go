// Package metrics exposes Prometheus instrumentation for the Go gateway.
//
// Wiring shape (symmetric with engine_cpp/src/metrics/):
//
//   - 4 baseline metrics matching the ldz #46 observability contract:
//     aegis_gateway_up, aegis_gateway_active_sessions,
//     aegis_gateway_rpc_total, aegis_gateway_rpc_duration_seconds.
//   - UnaryInterceptor + StreamInterceptor for grpc-go that populate
//     rpc_total + rpc_duration_seconds around every RPC handler.
//   - Handler() returns the http.Handler that cmd/gateway/main.go
//     wires onto its :8081 third server (K8s controller-runtime
//     convention: :8080 main HTTP + :8081 mgmt/metrics).
//
// Registry strategy: one process-global prometheus.Registry defined
// in init() and reused by every metric. Callers write to exported
// vars; the HTTP exposer serves from the same registry. This keeps
// the surface narrow and testable (tests can swap in their own
// registry via the RegistryForTest helper, if added later).
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

// Package-level registry. Exported via Handler() for the HTTP exposer.
var registry = prometheus.NewRegistry()

// Four baseline metrics per ldz #46. Label cardinality is bounded at
// design time: method comes from a finite set of gRPC service methods,
// status is coarse "ok" vs "error" (same convention as the engine's
// instrumentation — per-code fan-out can follow if operators ask).

// Up — 1 when the gateway process is up and all servers have begun
// listening. Set exactly once at startup.
var Up = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "aegis_gateway_up",
	Help: "1 when the gateway process is up and all listeners are bound.",
})

// ActiveSessions — point-in-time count of sessions in the registry
// (mirrors the gateway_go/internal/session.Registry.Len view).
// Updated on a short poll from main.go rather than on every session
// event because the registry has no change-notification channel.
var ActiveSessions = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "aegis_gateway_active_sessions",
	Help: "Number of active sessions currently held by the registry.",
})

// RpcTotal — gRPC RPC count by {method, status}. Populated by the
// Unary + Stream interceptors.
var RpcTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "aegis_gateway_rpc_total",
		Help: "Total gRPC RPCs handled by the gateway, labelled by method and terminal status.",
	},
	[]string{"method", "status"},
)

// RpcDurationSeconds — handler duration. Buckets mirror the engine's
// histogram boundaries so Prometheus rate() queries can compose across
// services without bucket-realignment surprises.
var RpcDurationSeconds = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "aegis_gateway_rpc_duration_seconds",
		Help:    "Handler duration in seconds, labelled by method.",
		Buckets: []float64{0.001, 0.01, 0.1, 1.0, 5.0, 30.0, 120.0, 600.0},
	},
	[]string{"method"},
)

// -----------------------------------------------------------------------------
// Domain metrics (Phase 4d / ROADMAP).
//
// Infrastructure metrics above answer "is the gateway healthy"; these
// answer "is the product doing the work it's supposed to do". Both
// feed the same Prometheus scrape, but the domain metrics are what
// SLOs are defined against (ARCH §10.4) — the burn-rate denominator
// for the Phase 4c-5b AnalysisTemplate lives here.
// -----------------------------------------------------------------------------

// HintsEmittedTotal — count of PrompterHint events the gateway has
// broadcast to viewers, labelled by origin.
//
//   source="retriever"  — engine-origin RAG hits flowing from
//                         `Pipeline.runEgress` on the `EgressMessage_Hint`
//                         variant (ADR-0019 retriever path)
//   source="officer"    — staff-authored hints via the
//                         SendOfficerHint RPC handler (Phase 3c Slice 7)
//
// Useful for:
//   - "is the retriever actually firing?" — alert on the ratio
//     dropping to zero mid-meeting
//   - "what fraction of hints are staff overrides?" — signals whether
//     the retriever is under-performing vs the chief-of-staff's
//     manual interventions
var HintsEmittedTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "aegis_gateway_hints_emitted_total",
		Help: "PrompterHint events broadcast to viewers, labelled by " +
			"origin (retriever = engine RAG hit, officer = staff-authored).",
	},
	[]string{"source"},
)

// HostTransientLossTotal — count of host-side transient connection
// losses (ICE Disconnected transitions, which PAUSE the engine
// stream but keep the session alive for reconnect per ADR-0006).
//
// ICE Failed transitions are NOT counted here — those terminate
// the session (END_STREAM) and are covered by the session-ended
// telemetry. This counter captures the "laptop lid closed briefly"
// / "WiFi blip" / "moved between APs" class of events where the
// session is recoverable.
//
// Useful for:
//   - environment-quality signal: a meeting with high transient-loss
//     count likely has a flaky network and the transcript / RAG
//     confidence interpretation should weight that
//   - diagnostic pattern: sudden spike in transient_loss across all
//     active sessions points at gateway-side ICE stack regression,
//     not per-session network conditions
var HostTransientLossTotal = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "aegis_gateway_host_transient_loss_total",
		Help: "Host-side ICE Disconnected transitions that PAUSE the " +
			"engine stream but keep the session alive (transient loss " +
			"per ADR-0006). Terminal ICE Failed transitions are NOT " +
			"counted here.",
	},
)

func init() {
	registry.MustRegister(
		Up,
		ActiveSessions,
		RpcTotal,
		RpcDurationSeconds,
		HintsEmittedTotal,
		HostTransientLossTotal,
	)
}

// Handler returns the HTTP handler that serves /metrics. cmd/gateway
// attaches this to its third http.Server on :8081.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// UnaryInterceptor instruments every unary gRPC handler. Install with
// grpc.NewServer(grpc.UnaryInterceptor(metrics.UnaryInterceptor())).
func UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		recordRPC(info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

// StreamInterceptor instruments every streaming gRPC handler. Install with
// grpc.NewServer(grpc.StreamInterceptor(metrics.StreamInterceptor())).
// Stream duration is measured handler-entry to handler-exit, which for
// long-lived bidi streams (e.g., transcript fan-out to viewers) captures
// the total stream lifetime — useful for session-duration capacity
// planning alongside the existing ActiveSessions gauge.
func StreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		err := handler(srv, ss)
		recordRPC(info.FullMethod, err, time.Since(start))
		return err
	}
}

func recordRPC(method string, err error, elapsed time.Duration) {
	// Coarse ok/error labels bound cardinality. Per-gRPC-code expansion
	// via google.golang.org/grpc/status is a trivial future change once
	// operators ask for the finer breakdown.
	lbl := "ok"
	if err != nil {
		lbl = "error"
	}
	RpcTotal.WithLabelValues(method, lbl).Inc()
	RpcDurationSeconds.WithLabelValues(method).Observe(elapsed.Seconds())
}
