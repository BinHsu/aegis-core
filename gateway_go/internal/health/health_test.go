package health_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/BinHsu/aegis-core/gateway_go/internal/health"
)

// probe drives one /readyz request against the gate and returns the
// status code.
func probe(t *testing.T, r *health.Readiness) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr.Code
}

// TestReadiness_StateMachine_BVA walks the readiness state machine
// through every meaningful transition point. The boundary here is the
// STATE itself: the gate has three load-bearing states and the bug
// class is "flip in the wrong direction / flip at the wrong time".
//
// BVA mapping for a state machine (B-1 / B / B+1 as transition points):
//
//   - initial (pre-listen) ......... not ready -> 503   [B-1: before bind]
//   - after SetReady(true) ......... ready     -> 200   [B  : serving]
//   - after SetReady(false) ........ not ready -> 503   [B+1: draining]
//
// The post-drain 503 is NOT the same assertion as the pre-listen 503:
// it proves the flip is reversible and that the drain genuinely
// re-closes the gate, which is the property the orchestrator depends on
// to stop routing new traffic on SIGTERM (ADR-0006).
func TestReadiness_StateMachine_BVA(t *testing.T) {
	t.Parallel()
	r := health.NewReadiness()

	steps := []struct {
		name     string
		set      func()
		wantCode int
		wantFlag bool
	}{
		{
			name:     "pre-listen (default, not ready)",
			set:      func() {}, // no SetReady call — exercises the zero value
			wantCode: http.StatusServiceUnavailable,
			wantFlag: false,
		},
		{
			name:     "serving (after SetReady(true))",
			set:      func() { r.SetReady(true) },
			wantCode: http.StatusOK,
			wantFlag: true,
		},
		{
			name:     "draining (after SetReady(false))",
			set:      func() { r.SetReady(false) },
			wantCode: http.StatusServiceUnavailable,
			wantFlag: false,
		},
	}

	for _, step := range steps {
		step.set()
		if got := probe(t, r); got != step.wantCode {
			t.Errorf("%s: /readyz status = %d, want %d", step.name, got, step.wantCode)
		}
		if got := r.Ready(); got != step.wantFlag {
			t.Errorf("%s: Ready() = %v, want %v", step.name, got, step.wantFlag)
		}
	}
}

// TestReadiness_DefaultNotReady asserts the zero value is not-ready
// WITHOUT any SetReady call. This is the load-bearing safety property:
// if NewReadiness ever started ready, /readyz would answer 200 during
// the window between mux wiring and listener bind, and the orchestrator
// would route traffic at a process that cannot yet serve it.
func TestReadiness_DefaultNotReady(t *testing.T) {
	t.Parallel()
	r := health.NewReadiness()
	if r.Ready() {
		t.Fatal("NewReadiness must start not-ready")
	}
	if got := probe(t, r); got != http.StatusServiceUnavailable {
		t.Errorf("default /readyz status = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// TestReadiness_Idempotent asserts repeated SetReady calls with the
// same value do not corrupt or toggle the state — a stray duplicate
// flip (e.g. a double-fired shutdown path) must be a no-op, not a
// surprise transition.
func TestReadiness_Idempotent(t *testing.T) {
	t.Parallel()
	r := health.NewReadiness()

	r.SetReady(true)
	r.SetReady(true)
	if !r.Ready() {
		t.Error("two SetReady(true) calls must leave the gate ready")
	}

	r.SetReady(false)
	r.SetReady(false)
	if r.Ready() {
		t.Error("two SetReady(false) calls must leave the gate not-ready")
	}
}

// TestReadiness_ConcurrentSetReady hammers SetReady from many
// goroutines while ServeHTTP reads concurrently. It proves the atomic
// correctness contract: no data race (run under -race), and the gate
// always settles to a well-defined terminal state determined by the
// last write — never a torn or undefined value.
//
// The final SetReady(true) is sequenced AFTER the racing writers join,
// so the terminal state is deterministic regardless of interleaving.
func TestReadiness_ConcurrentSetReady(t *testing.T) {
	t.Parallel()
	r := health.NewReadiness()

	const goroutines = 64
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Alternate true/false; concurrent readers run in parallel.
				r.SetReady((id+j)%2 == 0)
				_ = r.Ready()
				probe(t, r) // ServeHTTP races against the writers above
			}
		}(i)
	}
	wg.Wait()

	// Deterministic terminal write after all racers have joined.
	r.SetReady(true)
	if !r.Ready() {
		t.Fatal("after concurrent churn + final SetReady(true), gate must be ready")
	}
	if got := probe(t, r); got != http.StatusOK {
		t.Errorf("terminal /readyz status = %d, want %d", got, http.StatusOK)
	}
}
