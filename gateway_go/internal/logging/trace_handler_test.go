package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/BinHsu/aegis-core/gateway_go/internal/logging"
)

// TestTraceContextHandler_SpanBoundary exercises the span-context
// boundary in the slog handler — the load-bearing decision is
// SpanContext.IsValid(): below the boundary (no span / all-zero IDs)
// the trace fields are OMITTED entirely; at-and-above the boundary
// (a real recording span) trace_id + span_id are populated.
//
// BVA framing for the span-validity boundary B = "valid SpanContext":
//   - B-1  no span in ctx          → IsValid() == false → fields omitted
//   - B-1  explicit all-zero IDs   → IsValid() == false → fields omitted
//          (zero TraceID/SpanID is the same invalid code path; covered
//          here as the distinct "looks like a span but isn't" case so
//          a future regression that emits all-zero hex is caught)
//   - B    real SDK-started span   → IsValid() == true  → fields present
func TestTraceContextHandler_SpanBoundary(t *testing.T) {
	t.Parallel()

	t.Run("no span in ctx: trace fields omitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"test-pod", "test-node",
		)
		logger := slog.New(handler)

		logger.InfoContext(context.Background(), "no span")

		fields := decodeLine(t, buf.Bytes())
		if _, ok := fields["trace_id"]; ok {
			t.Errorf("trace_id should be absent: %v", fields)
		}
		if _, ok := fields["span_id"]; ok {
			t.Errorf("span_id should be absent: %v", fields)
		}
		if got := fields["pod"]; got != "test-pod" {
			t.Errorf("pod: got %v, want test-pod", got)
		}
		if got := fields["node"]; got != "test-node" {
			t.Errorf("node: got %v, want test-node", got)
		}
	})

	t.Run("all-zero span context: trace fields omitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"", "",
		)
		logger := slog.New(handler)

		// An explicitly zero-valued SpanContext — the boundary case
		// that distinguishes "no span" from "a span object whose IDs
		// are all zero". Both must omit the fields; an all-zero
		// trace_id is useless noise in Tempo/Loki.
		ctx := contextWithZeroSpan(context.Background())
		logger.InfoContext(ctx, "zero span")

		fields := decodeLine(t, buf.Bytes())
		if _, ok := fields["trace_id"]; ok {
			t.Errorf("trace_id should be absent for all-zero span: %v", fields)
		}
		if _, ok := fields["span_id"]; ok {
			t.Errorf("span_id should be absent for all-zero span: %v", fields)
		}
	})

	t.Run("active span: trace_id + span_id populated", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"", "",
		)
		logger := slog.New(handler)

		tp := sdktrace.NewTracerProvider()
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		ctx, span := tp.Tracer("test").Start(context.Background(), "op")
		defer span.End()

		logger.InfoContext(ctx, "with span")

		fields := decodeLine(t, buf.Bytes())
		traceID, ok := fields["trace_id"].(string)
		if !ok || traceID == "" || isAllZeroHex(traceID) {
			t.Errorf("trace_id should be non-zero hex: got %v", fields["trace_id"])
		}
		spanID, ok := fields["span_id"].(string)
		if !ok || spanID == "" || isAllZeroHex(spanID) {
			t.Errorf("span_id should be non-zero hex: got %v", fields["span_id"])
		}
	})
}

// TestTraceContextHandler_PodNodeBoundary covers the pod/node emit
// boundary B = "env-derived identifier is empty". An empty string
// means the Downward API env var was unset — the field must be
// omitted, not emitted as "". A non-empty value must appear verbatim.
func TestTraceContextHandler_PodNodeBoundary(t *testing.T) {
	t.Parallel()

	t.Run("both empty: pod and node omitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"", "",
		)
		slog.New(handler).InfoContext(context.Background(), "no downward api")

		fields := decodeLine(t, buf.Bytes())
		if _, ok := fields["pod"]; ok {
			t.Errorf("pod should be absent when empty: %v", fields)
		}
		if _, ok := fields["node"]; ok {
			t.Errorf("node should be absent when empty: %v", fields)
		}
	})

	t.Run("only pod set: node omitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"pod-x", "",
		)
		slog.New(handler).InfoContext(context.Background(), "pod only")

		fields := decodeLine(t, buf.Bytes())
		if got := fields["pod"]; got != "pod-x" {
			t.Errorf("pod: got %v, want pod-x", got)
		}
		if _, ok := fields["node"]; ok {
			t.Errorf("node should be absent when empty: %v", fields)
		}
	})

	t.Run("both set: both emitted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		handler := logging.NewTraceContextHandler(
			slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
			"pod-y", "node-z",
		)
		slog.New(handler).InfoContext(context.Background(), "both set")

		fields := decodeLine(t, buf.Bytes())
		if got := fields["pod"]; got != "pod-y" {
			t.Errorf("pod: got %v, want pod-y", got)
		}
		if got := fields["node"]; got != "node-z" {
			t.Errorf("node: got %v, want node-z", got)
		}
	})
}

// TestTraceContextHandler_Enabled confirms Enabled delegates to the
// inner handler — the level boundary B = handler threshold. With the
// inner handler set to LevelInfo: B-1 (Debug) is disabled, B (Info)
// and B+1 (Warn) are enabled.
func TestTraceContextHandler_Enabled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler := logging.NewTraceContextHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}),
		"", "",
	)
	ctx := context.Background()
	if handler.Enabled(ctx, slog.LevelDebug) {
		t.Error("Debug (B-1) should be disabled at LevelInfo threshold")
	}
	if !handler.Enabled(ctx, slog.LevelInfo) {
		t.Error("Info (B) should be enabled at LevelInfo threshold")
	}
	if !handler.Enabled(ctx, slog.LevelWarn) {
		t.Error("Warn (B+1) should be enabled at LevelInfo threshold")
	}
}

// TestTraceContextHandler_WithAttrs is a regression guard: if WithAttrs
// returned the bare inner handler instead of re-wrapping, pod/node
// injection would silently stop after any logger.With(...) call.
func TestTraceContextHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler := logging.NewTraceContextHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		"test-pod", "test-node",
	)
	logger := slog.New(handler).With("pkg", "gateway")

	logger.InfoContext(context.Background(), "with attrs")

	fields := decodeLine(t, buf.Bytes())
	if fields["pod"] != "test-pod" {
		t.Errorf("pod after With(): got %v, want test-pod", fields["pod"])
	}
	if fields["node"] != "test-node" {
		t.Errorf("node after With(): got %v, want test-node", fields["node"])
	}
	if fields["pkg"] != "gateway" {
		t.Errorf("With() attr: got %v, want gateway", fields["pkg"])
	}
}

// TestTraceContextHandler_WithGroup confirms WithGroup keeps the
// wrapper intact — pod/node are still injected, nested under the
// group name per slog semantics.
func TestTraceContextHandler_WithGroup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	handler := logging.NewTraceContextHandler(
		slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}),
		"test-pod", "test-node",
	)
	logger := slog.New(handler).WithGroup("grp")

	logger.InfoContext(context.Background(), "with group")

	fields := decodeLine(t, buf.Bytes())
	grp, ok := fields["grp"].(map[string]any)
	if !ok {
		t.Fatalf("expected a 'grp' group object, got %v", fields["grp"])
	}
	if grp["pod"] != "test-pod" {
		t.Errorf("pod after WithGroup(): got %v, want test-pod", grp["pod"])
	}
	if grp["node"] != "test-node" {
		t.Errorf("node after WithGroup(): got %v, want test-node", grp["node"])
	}
}

func decodeLine(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return m
}

// contextWithZeroSpan returns a context carrying an explicitly
// zero-valued SpanContext — TraceID and SpanID are all-zero, so
// SpanContext.IsValid() reports false. This is the "looks like a
// span but isn't" boundary case distinct from "no span at all".
func contextWithZeroSpan(ctx context.Context) context.Context {
	return trace.ContextWithSpanContext(ctx, trace.SpanContext{})
}

func isAllZeroHex(hex string) bool {
	for _, c := range hex {
		if c != '0' {
			return false
		}
	}
	return true
}
