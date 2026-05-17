// gateway_go/internal/tracing/allowlist_test.go
//
// Coverage for the ADR-0005 R4 enforcement layer. Critical-path
// tests (the ones that catch regressions of the actual policy) are
// the deny-list cases — what the gateway MUST drop on the wire.
// Allow-list cases are sanity checks that observability data still
// makes it through.

package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// captureExporter is a stub SpanExporter that records every spans
// slice passed to ExportSpans, so tests can inspect the post-filter
// span shape AllowlistExporter forwarded.
type captureExporter struct {
	calls [][]sdktrace.ReadOnlySpan
}

func (c *captureExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	// Defensive copy — sdktrace recycles span slices internally.
	cp := make([]sdktrace.ReadOnlySpan, len(spans))
	copy(cp, spans)
	c.calls = append(c.calls, cp)
	return nil
}

func (c *captureExporter) Shutdown(_ context.Context) error { return nil }

// fakeSpan builds a single ReadOnlySpan with the supplied attribute
// set. Goes through tracetest.SpanStub.Snapshot so the result IS a
// valid sdktrace.ReadOnlySpan that AllowlistExporter handles
// identically to one the SDK produced.
func fakeSpan(attrs ...attribute.KeyValue) sdktrace.ReadOnlySpan {
	return tracetest.SpanStub{
		Name:       "test-span",
		Attributes: attrs,
	}.Snapshot()
}

// pulledKeys returns the attribute keys present on the span (string
// form), in iteration order. Used to assert the allowlist preserved
// vs. dropped the right keys.
func pulledKeys(span sdktrace.ReadOnlySpan) []string {
	attrs := span.Attributes()
	out := make([]string, 0, len(attrs))
	for _, kv := range attrs {
		out = append(out, string(kv.Key))
	}
	return out
}

// --- Allowed keys --------------------------------------------------------

func TestAllowsRPCObservabilityKeys(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "aegis.v1.Gateway"),
		attribute.String("rpc.method", "CreateMeeting"),
		attribute.Int("rpc.grpc.status_code", 0),
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 4 {
		t.Errorf("expected 4 attrs preserved, got %d: %v", len(got), got)
	}
}

func TestAllowsAegisDomainNamespace(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		attribute.String("aegis.session_id_hash", "abcd1234"),
		attribute.String("aegis.tenant_id", "tenant-alpha"),
		attribute.String("aegis.deploy_mode", "cloud"),
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 3 {
		t.Errorf("expected 3 aegis.* attrs preserved, got %d: %v", len(got), got)
	}
}

// --- Critical denies -----------------------------------------------------

func TestDropsTranscriptText(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	// The exact regression ADR-0005 R4 protects against: a future
	// instrumentation labels a span with the literal transcript.
	span := fakeSpan(
		attribute.String("rpc.method", "StreamTranscribe"), // safe
		attribute.String("transcript_text", "we discussed the merger"),
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 1 || got[0] != "rpc.method" {
		t.Errorf("expected only rpc.method preserved, got %v", got)
	}
}

func TestDropsPCMAttribute(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		attribute.String("audio.pcm_sample_count", "16000"),
		attribute.String("rpc.method", "WriteRTPPayload"),
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 1 || got[0] != "rpc.method" {
		t.Errorf("audio.* / *pcm* must be dropped; got %v", got)
	}
}

func TestDropsEnduserNamespace(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		attribute.String("enduser.id", "cognito-sub-abc"),
		attribute.String("enduser.role", "host"),
		attribute.String("rpc.service", "aegis.v1.Gateway"), // safe
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 1 || got[0] != "rpc.service" {
		t.Errorf("enduser.* must be dropped; got %v", got)
	}
}

func TestDropsRequestResponseBodies(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		attribute.String("rpc.request.body", "..."),
		attribute.String("http.response.body", "..."),
		attribute.String("rpc.method", "CreateMeeting"), // safe
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 1 || got[0] != "rpc.method" {
		t.Errorf("*.request.* / *.response.* must be dropped; got %v", got)
	}
}

// --- Mixed (the real-world span shape) -----------------------------------

func TestMixedSpan(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan(
		// Safe — RPC observability + service identity + domain key.
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "aegis.v1.Gateway"),
		attribute.String("rpc.method", "CreateMeeting"),
		attribute.Int("rpc.grpc.status_code", 0),
		attribute.String("service.name", "aegis-core-gateway"),
		attribute.String("aegis.tenant_id", "tenant-alpha"),
		// Should be dropped — accidental payload + identity leak.
		attribute.String("transcript_text", "we discussed Q4"),
		attribute.String("enduser.email", "user@example.com"),
		attribute.String("rpc.request.body_size", "4096"),
	)
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	got := pulledKeys(cap.calls[0][0])
	if len(got) != 6 {
		t.Errorf("expected 6 safe attrs preserved, got %d: %v", len(got), got)
	}
	for _, k := range []string{"transcript_text", "enduser.email", "rpc.request.body_size"} {
		for _, g := range got {
			if g == k {
				t.Errorf("%q leaked through allowlist", k)
			}
		}
	}
}

// --- Empty / boundary ----------------------------------------------------

func TestEmptySpansSliceIsPassthrough(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)
	if err := exp.ExportSpans(context.Background(), nil); err != nil {
		t.Fatalf("ExportSpans(nil): %v", err)
	}
	if len(cap.calls) != 1 {
		t.Errorf("expected exactly one downstream call, got %d", len(cap.calls))
	}
}

func TestSpanWithNoAttributesPassesThrough(t *testing.T) {
	cap := &captureExporter{}
	exp := NewAllowlistExporter(cap)

	span := fakeSpan() // no attrs
	if err := exp.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{span}); err != nil {
		t.Fatalf("ExportSpans: %v", err)
	}
	if got := pulledKeys(cap.calls[0][0]); len(got) != 0 {
		t.Errorf("expected 0 attrs, got %v", got)
	}
}
