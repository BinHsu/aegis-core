// Package tracing wires OpenTelemetry distributed tracing for the
// Go gateway per ADR-0005 R4 and Phase 4d ROADMAP §"OTLP exporter".
//
// What this gives the operator that infrastructure metrics
// (aegis_gateway_rpc_*) cannot:
//
//   - Per-request span trees: a single CreateMeeting → Engine.Health
//     → Session.Create → JWT.Issue chain shows up as parent-child
//     spans in Tempo / Jaeger, so latency outliers can be
//     blamed on the actual hop, not just "p99 went up"
//   - Cross-service correlation via traceparent header: a viewer
//     event going from gateway-broadcast to engine-egress can be
//     stitched into one trace as soon as the engine side adopts
//     OpenTelemetry too (separate slice — opentelemetry-cpp is a
//     hermetic-dep / rules_foreign_cc story we're punting on)
//
// Two responsibilities, both wired here:
//
//   1. Tracer-provider lifecycle: build the SDK with a deploy-mode-
//      appropriate exporter, return a shutdown closure for orderly
//      span flush at process exit.
//   2. ADR-0005 R4 attribute allowlist: wrap the exporter so any
//      span attribute outside the allowlist is dropped before the
//      span leaves this process. Defends against a future
//      span.SetAttributes("transcript_text", ...) regression
//      silently leaking PCM-derived content through OTLP.
//
// Wire model:
//
//   main.tsx-equivalent (cmd/gateway/main.go) calls Init(ctx, mode)
//   exactly once at startup, defers shutdown(). Then otelgrpc
//   interceptors auto-create spans on every gRPC handler. The
//   Tracer can be borrowed via otel.Tracer("aegis.gateway") from
//   anywhere in the package tree if a handler wants to add a custom
//   child span.

package tracing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// DeployMode mirrors the auth provider's mode tag — we accept it as a
// string so this package doesn't pull internal/auth into its dep graph
// (one-way dep stays clean: cmd/gateway/main.go is the only crossing).
type DeployMode string

const (
	ModeLocal     DeployMode = "local"
	ModeCloud     DeployMode = "cloud"
	ModeCloudTest DeployMode = "cloud-test"
)

// ServiceName is the value used for the `service.name` resource
// attribute that distinguishes gateway spans from engine spans
// (whenever the engine side adopts OTLP).
const ServiceName = "aegis-gateway"

// Init builds and registers a global TracerProvider for the gateway.
//
// Returns a shutdown closure that callers MUST defer. The closure
// flushes any in-flight spans + closes the exporter; running it from
// signal-driven shutdown ensures pending spans reach the collector
// before the process exits.
//
// Mode selects the exporter:
//
//   - ModeLocal       → stdout (developer-readable JSON traces in the
//                       process log; no network, no auth)
//   - ModeCloud       → OTLP gRPC to the OTEL_EXPORTER_OTLP_ENDPOINT
//                       env-configured collector (LDZ-side decision —
//                       X-Ray collector vs Tempo vs etc.)
//   - ModeCloudTest   → stdout (same as Local — integration tests
//                       benefit from inline trace inspection)
//
// Failure of the OTLP exporter to dial the collector at Init time is
// NOT fatal — the SDK will retry per its default backoff, and span
// emission falls back to dropping silently rather than blocking the
// gateway. Logged as a warning via the returned error so operators
// can see it but the process keeps serving requests. Trace data being
// best-effort is the right posture for an SLO-first observability
// stack: dropped traces are a degradation, not an outage.
func Init(ctx context.Context, mode DeployMode) (shutdown func(context.Context) error, err error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(versionFromEnv()),
			semconv.DeploymentEnvironment(string(mode)),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: resource.New: %w", err)
	}

	exporter, err := buildExporter(ctx, mode)
	if err != nil {
		return nil, err
	}

	// Wrap with the ADR-0005 R4 allowlist BEFORE the BatchSpanProcessor
	// so dropped attributes never reach the network. Attributes are
	// filtered span-by-span at export time.
	filteredExporter := NewAllowlistExporter(exporter)

	bsp := sdktrace.NewBatchSpanProcessor(
		filteredExporter,
		sdktrace.WithMaxExportBatchSize(256),
		sdktrace.WithBatchTimeout(5*time.Second),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(bsp),
		sdktrace.WithResource(res),
		// AlwaysSample for staging is fine — meeting-grade traffic is
		// low-cardinality. When prod cuts in, switch to a parent-based
		// ratio sampler driven by OTEL_TRACES_SAMPLER env per OTel
		// convention. Sampler is a separate ADR-scale decision.
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	// Propagator: W3C TraceContext + Baggage. The grpc client/server
	// otelgrpc interceptors use this to extract / inject
	// `traceparent` headers across the network so a span tree
	// continues across services.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// buildExporter selects the SDK exporter for the given deploy mode.
func buildExporter(ctx context.Context, mode DeployMode) (sdktrace.SpanExporter, error) {
	switch mode {
	case ModeLocal, ModeCloudTest:
		// stdouttrace.WithPrettyPrint would expand each span into
		// multi-line JSON — useful for one-off debug, noisy in a
		// running gateway. Compact JSON keeps log lines greppable.
		return stdouttrace.New()

	case ModeCloud:
		// OTEL_EXPORTER_OTLP_ENDPOINT (and TLS / headers / etc.) are
		// read from env per OTel convention; the SDK looks them up
		// inside otlptracegrpc.New. The gateway Deployment env
		// (apps/staging/aegis-gateway/rollout.yaml) sets the
		// endpoint to whatever LDZ provisions — Phase 4d C-Obs-2
		// follow-up — until then this falls through to localhost,
		// which simply errors on every export attempt (logged).
		return otlptracegrpc.New(ctx)

	case "":
		return nil, errors.New("tracing.Init: DEPLOY_MODE empty (set local/cloud/cloud-test)")

	default:
		return nil, fmt.Errorf("tracing.Init: unrecognized DEPLOY_MODE %q", mode)
	}
}

// versionFromEnv lets the deployment override the service version
// via env at runtime (typically the git SHA injected by the OCI
// build). Falls back to "dev" when unset — fine for LOCAL.
func versionFromEnv() string {
	if v := os.Getenv("AEGIS_GATEWAY_VERSION"); v != "" {
		return v
	}
	return "dev"
}

// ExtraAttributes is a small helper for handlers that want to drop
// an additional Aegis-domain attribute on the active span without
// importing the whole otel package. The returned slice is ready to
// pass to span.SetAttributes(...) — the allowlist filter is applied
// at export time, not here, so callers don't need to remember which
// keys are allowed.
//
// Provided as a convenience seam for future instrumentation; not
// currently used by any handler. Lands here so the import surface is
// cohesive when handlers do start adding tenant_id / session_id
// labels to their spans.
func ExtraAttributes(kvs ...attribute.KeyValue) []attribute.KeyValue {
	return kvs
}

// keyHasAegisDomain is a small predicate the allowlist uses to permit
// the `aegis.*` namespace generally — these are our own keys whose
// shape we audit at the SetAttributes call site, not the IdP / RPC
// surface that needs blanket protection.
func keyHasAegisDomain(k string) bool {
	return strings.HasPrefix(k, "aegis.")
}
