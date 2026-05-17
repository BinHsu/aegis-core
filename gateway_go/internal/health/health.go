// Package health provides the drain-aware readiness gate for the Go
// Gateway's HTTP surface.
//
// The Gateway exposes two probe endpoints on the :8080 mux:
//
//   - /healthz — LIVENESS. Always 200 once the process is up; failure
//     triggers a pod restart. Implemented inline in cmd/gateway via
//     makeHealthHandler (it also aggregates engine status as JSON).
//   - /readyz  — READINESS. Drain-aware: 503 before the listeners are
//     bound, 200 while serving, 503 during the SIGTERM drain. Failure
//     removes the pod from the Service endpoints WITHOUT restarting it,
//     so the orchestrator stops routing NEW traffic while in-flight
//     requests and streams finish (ADR-0006 §Graceful Shutdown).
//
// The Readiness type is the readiness gate: a single atomic bool that
// the entrypoint flips true once all three listeners are confirmed up
// and flips false as the first action of shutdown.
package health

import (
	"net/http"
	"sync/atomic"
)

// Readiness is the drain-aware readiness gate behind /readyz.
//
// It is created in the NOT-ready state (so /readyz answers 503 from the
// moment the mux is wired, before the listeners bind). The entrypoint
// calls SetReady(true) once all listeners are confirmed up, and
// SetReady(false) as the first action of shutdown — opening the 503
// drain window before the servers stop accepting, so the orchestrator
// routes new traffic elsewhere while in-flight work finishes.
//
// A Readiness is safe for concurrent use: SetReady and ServeHTTP may be
// called from any goroutine. The underlying atomic.Bool guarantees the
// state transition is observed atomically (no torn read of the flag).
type Readiness struct {
	ready atomic.Bool
}

// NewReadiness returns a Readiness in the not-ready state — /readyz
// answers 503 until SetReady(true) is called.
func NewReadiness() *Readiness {
	return &Readiness{}
}

// SetReady atomically flips the readiness state. true once all
// listeners are up; false at the start of the SIGTERM drain.
func (r *Readiness) SetReady(ready bool) {
	r.ready.Store(ready)
}

// Ready reports the current readiness state. Exposed for tests and for
// callers that want to branch on readiness without an HTTP round-trip.
func (r *Readiness) Ready() bool {
	return r.ready.Load()
}

// ServeHTTP implements http.Handler. It answers 200 with an empty body
// when ready and 503 otherwise. The body is intentionally empty: a
// readiness probe only consumes the status code, and keeping the
// response trivial avoids coupling orchestrator behaviour to a payload
// shape (unlike /healthz, which emits aggregated engine JSON).
func (r *Readiness) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if r.ready.Load() {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}
