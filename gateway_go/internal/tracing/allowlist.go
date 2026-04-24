// gateway_go/internal/tracing/allowlist.go
//
// ADR-0005 R4 enforcement at the OTLP egress boundary. Wraps a
// downstream SpanExporter; on every ExportSpans call, walks each
// span's attributes and DROPS any key not on the allowlist before
// forwarding the span to the underlying exporter.
//
// Threat model:
//
// The application code (handlers, interceptors, future engine
// instrumentation) calls span.SetAttributes(...) with whatever
// makes sense for that code path. Without a filter, a future
// regression — `span.SetAttributes(attribute.String("transcript",
// segment.Text))` — would silently leak meeting content through
// OTLP into the operator's span store, which is exactly the
// content ADR-0005 R3 wraps in `SensitiveBytes` to keep out of
// process logs.
//
// The allowlist gives the gateway a single auditable choke point:
// only attributes whose keys match `isAllowed(key)` reach the wire.
// New permitted keys must be added to this file (the diff is the
// audit). Unknown keys are dropped silently — operators see fewer
// attributes than the code emitted, but no PCM / transcript /
// email / sub leaks.
//
// Implementation choice — exporter wrap (not span processor):
//
// SpanProcessor.OnEnd receives a sdktrace.ReadOnlySpan; that
// interface is sealed (private() method) so we can't construct
// shadow instances directly. The SDK's tracetest.SpanStub is the
// only public type that implements ReadOnlySpan via Snapshot();
// wrapping at the exporter layer + going through SpanStub is the
// idiomatic OTel pattern for attribute filtering.

package tracing

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// AllowlistExporter wraps an underlying SpanExporter and drops any
// span attribute whose key is not on the allowlist before forwarding.
// Construct via NewAllowlistExporter; the zero value is invalid
// (downstream nil → ExportSpans panics).
type AllowlistExporter struct {
	downstream sdktrace.SpanExporter
}

// NewAllowlistExporter wraps `downstream` with the ADR-0005 R4
// filter. The downstream is typically the OTLP gRPC exporter or
// stdouttrace; this layer makes both safe by construction.
func NewAllowlistExporter(downstream sdktrace.SpanExporter) *AllowlistExporter {
	return &AllowlistExporter{downstream: downstream}
}

// ExportSpans filters every input span's attributes through
// isAllowed, then forwards the resulting (possibly attribute-trimmed)
// spans to the downstream exporter. Empty input is a passthrough.
func (e *AllowlistExporter) ExportSpans(
	ctx context.Context,
	spans []sdktrace.ReadOnlySpan,
) error {
	if len(spans) == 0 {
		return e.downstream.ExportSpans(ctx, spans)
	}

	filtered := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(span)
		stub.Attributes = filterAttributes(stub.Attributes)
		// Span events also carry attributes; same rule applies to
		// each event's attribute set so a future log-as-event
		// regression is covered by the same allowlist.
		for i := range stub.Events {
			stub.Events[i].Attributes = filterAttributes(stub.Events[i].Attributes)
		}
		filtered = append(filtered, stub.Snapshot())
	}
	return e.downstream.ExportSpans(ctx, filtered)
}

// Shutdown forwards to the downstream exporter; no buffering here.
func (e *AllowlistExporter) Shutdown(ctx context.Context) error {
	return e.downstream.Shutdown(ctx)
}

// filterAttributes returns the subset of attrs whose keys pass
// isAllowed. Order is preserved so reviewers reading a span in the
// trace UI see attributes in the same order app code added them.
func filterAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, kv := range attrs {
		if isAllowed(string(kv.Key)) {
			out = append(out, kv)
		}
	}
	return out
}

// isAllowed is the ADR-0005 R4 allowlist policy. Returns true iff
// the attribute key is safe to ship across the OTLP wire.
//
// Categories of permitted keys:
//
//   1. The `aegis.*` namespace — our own domain keys whose shapes
//      we audit at the SetAttributes call site, not via blanket
//      filter. (E.g. `aegis.session_id_hash`, `aegis.tenant_id`.)
//      Extending the namespace requires adding the key + a
//      Semgrep audit rule, NOT relaxing this filter.
//
//   2. RPC observability keys defined by OTel semantic conventions
//      — method name, service name, status code. These describe
//      the call; they do NOT carry payload.
//
//   3. HTTP observability keys (analogous to (2) for the HTTP
//      surface — /healthz, /lan-ip, /metrics).
//
//   4. Network keys that describe the transport, NOT the content.
//
//   5. OTel internal keys (`otel.*`, `telemetry.sdk.*`) — SDK
//      machinery; never includes payload.
//
// Anything else is dropped. In particular:
//
//   - rpc.request.* / rpc.response.* — would carry the marshalled
//     proto including TranscriptSegment.text and PCM payloads.
//   - enduser.* — Cognito sub / email / etc.
//   - any key containing `transcript`, `pcm`, `audio`, `payload` —
//     belt-and-suspenders against creative key naming.
func isAllowed(key string) bool {
	// Aegis-domain keys: blanket allow on the namespace because
	// SetAttribute call sites are auditable.
	if keyHasAegisDomain(key) {
		return true
	}
	if strings.HasPrefix(key, "otel.") || strings.HasPrefix(key, "telemetry.sdk.") {
		return true
	}

	// Hard deny for substrings that are payload-shaped regardless
	// of which standard sema-conv namespace they sit under. Catches
	// rpc.request.body / messaging.message.payload / kafka.transcript
	// / similar permutations a future SDK update might surface.
	if containsAny(key, deniedKeySubstrings) {
		return false
	}

	// Allowlist of safe stable keys from OTel semantic conventions.
	// Extend deliberately when a new instrumentation needs a new
	// key; review the new key against the threat model before adding.
	_, ok := allowedExactKeys[key]
	return ok
}

// allowedExactKeys is the precise enumeration of OTel semconv keys
// we permit on the OTLP wire. Lowercase string match against
// attribute.KeyValue.Key.
//
// Sourced from OpenTelemetry Semantic Conventions v1.26 (matching
// the semconv import used in tracing.go). Only the subset that
// describes "shape of the call" is permitted; payload-bearing keys
// are deliberately absent.
var allowedExactKeys = map[string]struct{}{
	// RPC dimensions — what call, where to, how it ended.
	"rpc.system":           {},
	"rpc.service":          {},
	"rpc.method":           {},
	"rpc.grpc.status_code": {},

	// HTTP dimensions for the gateway's plain-HTTP surface
	// (/healthz, /lan-ip, /metrics).
	"http.request.method":      {},
	"http.response.status_code": {},
	"http.route":                {},
	"url.path":                  {},
	"url.scheme":                {},

	// Network — peer identity (IP / port). Sufficient to debug
	// "which client IP was hammering us" without revealing
	// request bodies.
	"network.peer.address":     {},
	"network.peer.port":        {},
	"network.protocol.name":    {},
	"network.protocol.version": {},
	"server.address":           {},
	"server.port":              {},

	// Service identity — gateway vs engine, version, deploy env.
	"service.name":           {},
	"service.version":        {},
	"deployment.environment": {},

	// gRPC server status (used by otelgrpc when the server emits a
	// non-OK code). Categorical, not payload.
	"rpc.grpc.status_message": {},

	// Standard OTel error attribute (also categorical — type of
	// error, not the underlying request data).
	"error.type": {},
}

// deniedKeySubstrings catches payload-shaped naming regardless of
// where the key lives in semantic-convention space. Substring match
// over lowercase.
var deniedKeySubstrings = []string{
	"transcript",
	"pcm",
	"audio",
	"payload",
	".body",
	".request.",
	".response.",
	"enduser.",
	"email",
	"phone",
	"sub.",
	"sub-",
}

func containsAny(haystack string, needles []string) bool {
	lower := strings.ToLower(haystack)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}
