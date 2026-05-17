package profiling

import "testing"

// The testable boundary of this package is the fail-soft switch in
// Start: the Endpoint string. The "length" of Endpoint is the input
// domain with the meaningful boundary B = 0 (empty string):
//
//   B-1  — there is no string shorter than the empty string; the
//          off-by-one BELOW the boundary is instead the closest
//          distinct neighbour on the "non-empty" side at minimal
//          length: a single-character endpoint (len 1). It MUST take
//          the live-start path (and, with no reachable server, the
//          upstream client either errors or returns a handle — either
//          way Start must not panic and Stop must stay safe).
//   B    — Endpoint == "" (len 0): the no-op path. Start returns a
//          non-nil no-op Profiler, nil error.
//   B+1  — a normal-length endpoint string: also the live-start path,
//          confirming the switch is "empty vs non-empty", not
//          "len < N vs len >= N".
//
// The live-Pyroscope-start success path (a reachable ingest server
// returning a working *pyroscope.Profiler) is NOT unit-tested here:
// it needs a real Pyroscope HTTP server and is an integration-test-
// layer concern. Asserting against a fake network socket would prove
// nothing. What the UT layer CAN and DOES pin is the fail-soft
// contract — the boundary that actually ships disabled until the
// landing-zone provisions ingest.

// TestStart_EmptyEndpoint_BoundaryB is the boundary itself: an empty
// endpoint must yield a no-op Profiler and a nil error. This is the
// path the gateway takes today (AEGIS_PYROSCOPE_ENDPOINT unset).
func TestStart_EmptyEndpoint_BoundaryB(t *testing.T) {
	p, err := Start(Config{ApplicationName: "aegis-gateway", Endpoint: ""})
	if err != nil {
		t.Fatalf("Start with empty endpoint: want nil error, got %v", err)
	}
	if p == nil {
		t.Fatal("Start with empty endpoint: want non-nil Profiler, got nil")
	}
	if p.inner != nil {
		t.Errorf("Start with empty endpoint: want no-op Profiler (inner==nil), got inner=%v", p.inner)
	}
	// The no-op Profiler's Stop must be safe and report no error —
	// this is what makes the deferred Stop() in main.go unconditional.
	if err := p.Stop(); err != nil {
		t.Errorf("no-op Profiler Stop: want nil error, got %v", err)
	}
}

// TestStart_SingleCharEndpoint_BoundaryBMinus1 is the closest distinct
// neighbour below the boundary on the non-empty side: a len-1 endpoint
// must NOT take the no-op path — it crosses into the live-start path.
// With no Pyroscope server reachable, the upstream client may return
// an error or a handle; either is acceptable. What this test pins is
// that Start does not panic and the resulting handle's Stop stays safe.
func TestStart_SingleCharEndpoint_BoundaryBMinus1(t *testing.T) {
	p, err := Start(Config{ApplicationName: "aegis-gateway", Endpoint: "x"})
	if err != nil {
		// Live-start path was taken and the upstream client rejected
		// the bogus endpoint — correct fail-soft behaviour. The caller
		// (main.go) logs this as a warning, never fatal.
		return
	}
	// Live-start path returned a handle (pyroscope.Start is lenient
	// about unreachable servers — it dials lazily on flush). Stop must
	// still be safe to call.
	if p == nil {
		t.Fatal("Start with non-empty endpoint: got nil Profiler and nil error")
	}
	_ = p.Stop()
}

// TestStart_NormalEndpoint_BoundaryBPlus1 is a normal-length endpoint:
// it confirms the switch is "empty vs non-empty", not a length
// threshold. A multi-character endpoint must take the live-start path
// (inner set, or an error), never the no-op path.
func TestStart_NormalEndpoint_BoundaryBPlus1(t *testing.T) {
	p, err := Start(Config{
		ApplicationName: "aegis-gateway",
		Endpoint:        "http://127.0.0.1:4040",
	})
	if err != nil {
		// Upstream client rejected the endpoint at Start time —
		// acceptable; the no-op path was NOT taken, which is the point.
		return
	}
	if p == nil {
		t.Fatal("Start with normal endpoint: got nil Profiler and nil error")
	}
	if p.inner == nil {
		t.Error("Start with normal endpoint: took the no-op path (inner==nil); " +
			"the switch must be empty-vs-non-empty, not a length threshold")
	}
	_ = p.Stop()
}

// TestStop_NilReceiver pins the nil-receiver safety contract: the
// shutdown sequence in main.go defers Stop() on a Profiler that may be
// nil if Start itself was never reached. A nil-receiver Stop must not
// panic and must report no error.
func TestStop_NilReceiver(t *testing.T) {
	var p *Profiler // nil
	if err := p.Stop(); err != nil {
		t.Errorf("nil-receiver Stop: want nil error, got %v", err)
	}
}

// TestStop_ZeroValueProfiler pins that the zero-value Profiler (a
// non-nil pointer with inner==nil, exactly what Start returns on the
// empty-endpoint path) has a safe Stop. Calling it twice must also be
// safe — Stop is idempotent on a no-op Profiler.
func TestStop_ZeroValueProfiler(t *testing.T) {
	p := &Profiler{}
	if err := p.Stop(); err != nil {
		t.Errorf("zero-value Profiler Stop: want nil error, got %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("zero-value Profiler Stop (second call): want nil error, got %v", err)
	}
}
