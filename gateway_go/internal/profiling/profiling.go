// Package profiling wires continuous profiling for the Go gateway —
// the 4th observability signal alongside metrics (ADR-0033), traces
// (OTLP exporter slice), and structured logs.
//
// What this gives the operator that metrics + traces cannot:
//
//   - A metric tells you "p99 latency rose"; a trace tells you "the
//     Engine.Health hop is the slow one". Neither tells you WHICH
//     line of Go code is burning the CPU or allocating. Continuous
//     profiling closes that gap: a flame graph in Pyroscope, sampled
//     live from the running process, points at the function.
//   - Goroutine profiles surface leak shapes (a fan-out channel that
//     never drains, a pump goroutine that never exits) that no RPC
//     counter would catch.
//
// Three profile types are collected — CPU, alloc-objects, and
// goroutines — chosen to cover the gateway's two failure modes: it is
// an I/O orchestrator (goroutine + alloc pressure) far more than a
// compute box, but CPU profiles still catch a hot serialization path
// or a busy-loop regression.
//
// Fail-soft posture (ADR-0035): an empty or unreachable
// AEGIS_PYROSCOPE_ENDPOINT degrades profiling to a no-op. Start never
// blocks process startup and never touches the request path. This is
// deliberate — aegis-core ships this code before the landing-zone has
// provisioned the Grafana Cloud Pyroscope ingest, and a missing
// observability backend must be a degradation, not an outage. Same
// posture the tracing package takes for a missing OTLP collector.
//
// Wire model: cmd/gateway/main.go calls Start(cfg) exactly once after
// tracing.Init and before serving, and defers profiler.Stop() into the
// shutdown sequence. Start failure is logged as a warning, never fatal.
package profiling

import (
	"github.com/grafana/pyroscope-go"
)

// Config configures the Pyroscope client. An empty Endpoint disables
// profiling — Start then returns a no-op Profiler whose Stop is safe.
type Config struct {
	// ApplicationName is the Pyroscope application label. The gateway
	// passes tracing.ServiceName here so profiles, traces, and metrics
	// all join on the same service identity in Grafana.
	ApplicationName string

	// Endpoint is the Pyroscope ingest server address (the Grafana
	// Cloud Pyroscope URL in Cloud mode). Empty disables profiling.
	Endpoint string
}

// Profiler wraps the upstream pyroscope.Profiler so a nil-or-empty
// configuration produces a handle that satisfies the same Stop
// contract. The zero value (and a nil pointer) are both valid no-op
// Profilers — Stop short-circuits on either.
type Profiler struct {
	inner *pyroscope.Profiler
}

// Start begins continuous CPU, alloc-objects, and goroutine profiling
// against the configured Pyroscope endpoint.
//
// When Endpoint is empty, no upstream client is started and the
// returned Profiler is a no-op handle — this is the fail-soft path
// the gateway takes until the landing-zone provisions Pyroscope
// ingest. The returned error is non-nil only when a non-empty
// Endpoint was given but the upstream client failed to initialise;
// callers MUST treat that as a warning, not a fatal, so a transient
// ingest outage never blocks the gateway from serving requests.
func Start(cfg Config) (*Profiler, error) {
	if cfg.Endpoint == "" {
		return &Profiler{}, nil
	}
	p, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: cfg.ApplicationName,
		ServerAddress:   cfg.Endpoint,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileGoroutines,
		},
	})
	if err != nil {
		return nil, err
	}
	return &Profiler{inner: p}, nil
}

// Stop flushes any pending profile batches and shuts down the client.
// Safe to call on a nil receiver or on a no-op Profiler — both are
// expected outcomes of the fail-soft Start path, so the shutdown
// sequence in cmd/gateway/main.go can defer this unconditionally.
func (p *Profiler) Stop() error {
	if p == nil || p.inner == nil {
		return nil
	}
	return p.inner.Stop()
}
