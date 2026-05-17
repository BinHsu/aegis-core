package logging

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceContextHandler wraps a slog.Handler to inject trace correlation
// into every structured log record:
//
//   - trace_id / span_id — extracted from the OpenTelemetry span on the
//     record's context. With the otelgrpc interceptors already creating
//     a server span per inbound RPC (see cmd/gateway/main.go), any log
//     line emitted with that request's context becomes joinable to its
//     trace in Tempo. Before this handler, a gateway log line carried
//     no trace correlation at all.
//   - pod / node — static identifiers sourced once at startup from the
//     Kubernetes Downward API (AEGIS_POD_NAME / AEGIS_NODE_NAME env
//     vars). They let an operator pivot from a noisy pod's logs to that
//     pod's node without a kubectl round-trip.
//
// Records with no valid span context OMIT the trace fields entirely —
// emitting all-zero IDs would index a useless, high-noise label in
// Loki/Tempo. pod / node are likewise omitted when their env var is
// empty (e.g. a Local-mode run outside Kubernetes).
//
// The handler is dep-light by design: it depends only on log/slog and
// go.opentelemetry.io/otel/trace (the API surface, not the SDK), so the
// logging package stays a thin, auditable layer over the handler chain.
type TraceContextHandler struct {
	inner     slog.Handler
	pod, node string
}

// Compile-time assertion that the wrapper satisfies slog.Handler.
var _ slog.Handler = (*TraceContextHandler)(nil)

// NewTraceContextHandler wraps inner with trace + pod + node injection.
// Pass empty strings for pod / node to omit them — that is the correct
// posture for a Local-mode run where the Downward API env vars are
// unset.
func NewTraceContextHandler(inner slog.Handler, pod, node string) *TraceContextHandler {
	return &TraceContextHandler{inner: inner, pod: pod, node: node}
}

// Enabled delegates to the wrapped handler so the AEGIS_LOG_LEVEL
// threshold configured by New() is honored unchanged.
func (h *TraceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle adds pod, node, trace_id, and span_id attrs (each only when
// present and valid) and forwards the record to the inner handler.
func (h *TraceContextHandler) Handle(ctx context.Context, rec slog.Record) error {
	if h.pod != "" {
		rec.AddAttrs(slog.String("pod", h.pod))
	}
	if h.node != "" {
		rec.AddAttrs(slog.String("node", h.node))
	}
	// SpanContext.IsValid() is false for both "no span in ctx" and an
	// all-zero SpanContext — one branch covers both invalid cases, so
	// no all-zero hex ever reaches the log line.
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, rec)
}

// WithAttrs returns a new handler with the additional attrs applied to
// the inner handler. The pod / node fields propagate to the returned
// handler — without the re-wrap, a logger.With(...) call would silently
// drop trace correlation.
func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{
		inner: h.inner.WithAttrs(attrs),
		pod:   h.pod,
		node:  h.node,
	}
}

// WithGroup returns a new handler with a group applied to the inner
// handler. The pod / node fields propagate; with a group active the
// injected attrs nest under the group name, per slog semantics.
func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{
		inner: h.inner.WithGroup(name),
		pod:   h.pod,
		node:  h.node,
	}
}
