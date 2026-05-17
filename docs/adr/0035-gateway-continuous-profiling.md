# ADR-0035: Gateway continuous profiling — the 4th observability signal (Pyroscope)

| Field    | Value |
| -------- | ----- |
| Status   | Accepted |
| Date     | 2026-05-17 |
| Deciders | Project author |
| Context  | Phase 4d Observability Wiring. The gateway already emits three observability signals — Prometheus metrics (ADR-0033, C-Obs-1), OTLP traces (`gateway_go/internal/tracing/`), and structured logs (`gateway_go/internal/logging/`). The 4th signal — continuous profiling — was absent: nothing tells an operator *which Go function* is burning CPU or allocating, only that latency or memory rose. This ADR records the decision to add continuous profiling to the Go gateway via `github.com/grafana/pyroscope-go`, with a fail-soft posture so the code ships before the landing-zone provisions Grafana Cloud Pyroscope ingest. |
| Related  | ADR-0033 (Prometheus metrics endpoint — C-Obs-1; this ADR adds the signal that ADR-0033's "what's new downstream" list did not enumerate), Phase 4d ROADMAP §Observability Wiring, `gateway_go/internal/tracing/tracing.go` (the OTLP exporter slice — fail-soft posture mirrored here), ldz ADR-022 + ADR-023 (Grafana Cloud observability backend — Grafana Cloud also offers a Pyroscope tier), aegis-core#46 observability contract thread |

## Context

Metrics and traces answer "what" and "where" — *p99 latency rose*, *the `Engine.Health` hop is the slow one* — but neither answers "why, in the code". A hot serialization path, a busy-loop regression, a goroutine leak in a fan-out pump: these are invisible to RPC counters and span trees. Continuous profiling is the 4th signal that closes the gap. A flame graph sampled live from the running process points at the function.

The gateway is an I/O orchestrator (ADR-0017) more than a compute box, so its realistic failure modes are goroutine and allocation pressure rather than raw CPU. But CPU profiles still earn their place: a regression that turns an O(1) path into an O(n) one, or a tight retry loop, shows up there first.

The blocker to shipping this today is infrastructure ordering: the landing-zone has not yet provisioned a Grafana Cloud Pyroscope ingest endpoint. We do not want that to gate the aegis-core code. The same situation applied to the OTLP exporter slice (the collector endpoint was LDZ-provisioned later) and was solved with a fail-soft posture — a missing backend is a *degradation*, not an *outage* or a *startup blocker*. This ADR adopts the identical posture for profiling.

## Decision

**Add continuous profiling to the Go gateway as the 4th observability signal, using `github.com/grafana/pyroscope-go` as the client, wired fail-soft so an empty or unreachable endpoint degrades profiling to a no-op.**

### D1. Client library — `github.com/grafana/pyroscope-go`

Pyroscope is the open-source continuous-profiling project, now part of Grafana Labs; Grafana Cloud offers a managed Pyroscope tier alongside the Loki / Tempo / Mimir stack the landing-zone already targets (ldz ADR-022). Using the official `pyroscope-go` client keeps the signal on the same vendor as the rest of the observability backend — one Grafana pane of glass, profiles joinable to traces and metrics on the shared `service.name`.

This is consistent with the project's "prefer managed/upstream over DIY" posture for observability tooling: we do not hand-roll a pprof scraper.

### D2. Three profile types — CPU, alloc-objects, goroutines

The `profiling` package enables exactly three Pyroscope profile types:

- **CPU** — catches hot paths and busy-loop regressions.
- **alloc-objects** — allocation pressure, the gateway's more likely compute-side failure mode.
- **goroutines** — leak shapes (a fan-out channel that never drains, a pump goroutine that never exits) that no RPC counter surfaces.

Heap-in-use, mutex, and block profiles are deliberately out of scope for this first cut — see §Out of scope.

### D3. Fail-soft posture — empty endpoint is a no-op

A new package `gateway_go/internal/profiling` wraps the upstream client. `profiling.Start(cfg)`:

- When `cfg.Endpoint == ""` → no upstream client is started; a no-op `Profiler` handle is returned, `nil` error. This is the path the gateway takes today.
- When `cfg.Endpoint` is non-empty but the upstream client fails to initialise → `Start` returns the error; the caller logs it as a **warning, never fatal**.
- `Profiler.Stop()` is safe on a nil receiver and on a no-op `Profiler`, so the shutdown sequence in `cmd/gateway/main.go` can defer it unconditionally.

This mirrors `tracing.Init` exactly: profiling never blocks process startup and never touches the request path.

### D4. Configuration — `AEGIS_PYROSCOPE_ENDPOINT`

The Pyroscope ingest server address is read from `AEGIS_PYROSCOPE_ENDPOINT` (repo `AEGIS_*` env convention). Empty / unset = profiling disabled (no-op). The Pyroscope application name is `tracing.ServiceName` (`aegis-gateway`) so profiles, traces, and metrics all join on one service identity in Grafana.

`apps/staging/aegis-gateway/rollout.yaml` declares `AEGIS_PYROSCOPE_ENDPOINT` with an empty value — a placeholder that the fail-soft path handles cleanly and that becomes a one-line edit when the landing-zone provisions ingest.

### D5. Wire point

`cmd/gateway/main.go` calls `profiling.Start` once, **after** `tracing.Init` (shared service identity) and **before** the listeners come up (flame graph captures the full process lifetime), and defers `Stop()` into the shutdown sequence alongside `tracerShutdown`.

## Dependency on the landing-zone

The live profiling path needs a Grafana Cloud Pyroscope ingest endpoint, which the landing-zone has not yet provisioned. This is a cross-repo coordination item — it will be tracked as a **separate cross-repo issue** against `aegis-aws-landing-zone` (not opened by this ADR). Until that endpoint exists, `AEGIS_PYROSCOPE_ENDPOINT` stays empty and profiling is a no-op in every deploy mode — by design. The fail-soft posture (D3) is precisely what lets aegis-core ship the code now and flip the switch later with a one-line manifest edit.

## Out of scope

- **Engine-side (C++) profiling** — a separate signal/slice. The C++ engine would need a Pyroscope C++ integration or pprof-compatible exporter; not bundled here, mirroring how the OTLP-exporter slice landed gateway-side first and deferred the engine.
- **Heap-in-use / mutex / block profile types** — the three types in D2 cover the gateway's realistic failure modes; the others are addable as a one-line `ProfileTypes` change if an operator needs them.
- **Provisioning the Grafana Cloud Pyroscope ingest** — landing-zone scope (see §Dependency above).
- **Profiling-driven alerting / SLO gates** — profiles are a debugging signal, not an alert source; alerting stays metric-driven (ADR-0033 + Phase 4d C-Obs-2).
- **Sampling-rate / upload-cadence tuning** — the `pyroscope-go` defaults are accepted for the first cut; revisit if profile-upload volume becomes a Grafana Cloud free-tier cost concern.

## Alternatives Considered

### A. Defer profiling until the landing-zone provisions ingest

**Rejected.** This couples an aegis-core code slice to an infrastructure-provisioning timeline with no engineering reason. The fail-soft posture (D3) removes the coupling entirely: the code ships now, exercised by unit tests on the fail-soft boundary, and goes live with a one-line env edit. Identical reasoning to the OTLP-exporter slice.

### B. Roll our own `runtime/pprof` HTTP endpoint

**Rejected.** Go's `net/http/pprof` exposes on-demand pprof, but *continuous* profiling — periodic sampling, retention, flame-graph diffing across deploys — is exactly what Pyroscope provides as a managed product. Reimplementing the scrape + storage + UI layer is disproportionate effort against an upstream client that is ~50 lines to integrate. Consistent with the "prefer managed/upstream" posture used for the metrics and tracing slices.

### C. OpenTelemetry profiling signal (OTLP profiles)

**Deferred.** OTel has a profiling signal in development; once it is stable and the landing-zone's OTLP path supports it, profiling could route through the same exporter as traces. Today `pyroscope-go` is the production-ready path and Grafana Cloud is the committed backend. Revisit trigger: OTLP profiles reach GA *and* the landing-zone's collector accepts them.

## Consequences

### Positive

- The 4th observability signal lands; the gateway's debugging story is complete (metrics → traces → logs → profiles).
- Fail-soft means zero startup-risk and zero request-path cost when disabled — the current state in every deploy mode until ingest is provisioned.
- Profiles join traces and metrics on `service.name = aegis-gateway` — one Grafana correlation surface.
- The fail-soft switch is a clean unit-test boundary (empty vs non-empty endpoint), so the shipped-disabled code is still load-bearing-tested.

### Negative

- One more Go dependency (`pyroscope-go` + its `godeltaprof` submodule) on the gateway's build graph and CVE-scan surface.
- The live-Pyroscope-start path is not unit-tested — it needs a real ingest server and is an integration-test-layer concern (named explicitly, per CLAUDE.md Rule 2's escape-hatch clause).

### Neutral

- When disabled (the current state), the `pyroscope-go` client is imported but never started — a negligible binary-size cost, no runtime cost.

## Triggers to revisit

1. **Landing-zone provisions Grafana Cloud Pyroscope ingest** → set `AEGIS_PYROSCOPE_ENDPOINT` in `rollout.yaml`; profiling goes live with no code change.
2. **OTLP profiles signal reaches GA** → evaluate routing profiling through the OTLP exporter (Alternative C).
3. **Engine-side profiling becomes a debugging necessity** → separate ADR for the C++ Pyroscope integration.
4. **Profile-upload volume strains the Grafana Cloud free tier** → tune sampling rate / upload cadence / profile-type set.
