# ADR-0033: Engine + Gateway Prometheus Metrics Endpoint (C-Obs-1)

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-20                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4c / Phase 4d boundary slice C-Obs-1. Both aegis-core services (Go gateway, C++ engine) lacked Prometheus instrumentation. Landing-zone [#46](https://github.com/BinHsu/aegis-core/issues/46) established the observability contract (wide-open Prometheus Operator selectors, Grafana sidecar discovery, `app.kubernetes.io/part-of: aegis` label, ServiceMonitor CRD available). This ADR documents the aegis-core-side implementation. |
| Related  | ADR-0030 (Argo Rollouts — C-5b `AnalysisTemplate` will consume these metrics for SLO canary gates), ADR-0031 (mTLS — metrics scrape stays plaintext intra-cluster, separate port from the mTLS'd gRPC on :50051), `aegis-core#46` observability contract thread, ldz #54 platform surface contract, ldz ADR-022 + ADR-023 (observability backend reversal, see Revision below) |

## Revision 2026-04-21 — observability backend reversal (decisions intact)

Landing-zone reversed the observability backend from self-hosted kube-prometheus-stack to Grafana Cloud free tier via [ldz ADR-022](https://github.com/BinHsu/aegis-aws-landing-zone/blob/main/docs/decisions/022-observability-backend-grafana-cloud.md) + [ldz ADR-023](https://github.com/BinHsu/aegis-aws-landing-zone/blob/main/docs/decisions/023-observability-responsibility-model.md) (ldz PR #123 merged 2026-04-21 06:59 UTC). ACK'd by aegis-core on [#46](https://github.com/BinHsu/aegis-core/issues/46).

**Every decision in this ADR remains intact** — the backend change swaps *who does the scraping*, not *what aegis-core exposes*:

- `:8081/metrics` pull endpoint shape — unchanged. **Grafana Alloy** discovers and scrapes via the same `ServiceMonitor` CRD that kube-prometheus-stack's Prometheus Operator would have used.
- `ServiceMonitor` CRD label convention (`app.kubernetes.io/part-of: aegis`) — unchanged.
- Plaintext intra-cluster scrape posture — unchanged (Alloy runs in-cluster; the Grafana Cloud trust boundary is on the platform side, past Alloy's `remote_write`).
- NetworkPolicy ingress allow-rule target — shifts from "`monitoring` namespace running kube-prometheus-stack" to "whichever namespace ldz installs Alloy in" (likely `monitoring` or `observability`; confirm during ldz ADR-022 implementation PR).

**What's new downstream of this ADR**: aegis-core also ships four additional CRDs per the widened ldz #46 contract — `PrometheusRule`, `GrafanaDashboard`, `GrafanaContactPoint`, `GrafanaNotificationPolicyRoute`. These are tracked as Phase 4d **C-Obs-2** in ROADMAP.md, not folded into this ADR (C-Obs-1 is scoped to the pull endpoint; C-Obs-2 is the 5-CRD ship).

**Alt-B re-open trigger update**: "Deferred to Phase 4d consideration... Re-open when ldz ships an OpenTelemetry Collector" — Alloy supports OTLP receive natively, so the trigger is arguably already active. Decision to stay pull-scoped for C-Obs-1 still holds (it's the path of least resistance + matches the ServiceMonitor contract), but a future ADR-003X may evaluate OTLP-push from aegis-core → Alloy when there's a concrete benefit (multi-backend fan-out, or push-only environments).

Narrative passages below that reference "kube-prometheus-stack" are left as-written for git-blame archaeology; read them as "the scrape-first posture that Alloy now implements," not as current infra.

## Revision 2026-05-17 — log↔trace correlation companion (gateway)

This ADR's `:8081/metrics` endpoint and ADR-0005 R4's OTLP traces are two of the three observability pillars; logs were the third and, until this slice, the disconnected one — a gateway log line carried no identifier joinable to its trace in Tempo.

`gateway_go/internal/logging/trace_handler.go` closes that gap with a `TraceContextHandler` that wraps the env-configured slog handler (`AEGIS_LOG_FORMAT` / `AEGIS_LOG_LEVEL`). On every record it injects `trace_id` / `span_id` extracted from the OTel span on the request context (`trace.SpanFromContext`), plus static `pod` / `node` identifiers sourced once at startup from the Kubernetes Downward API env vars `AEGIS_POD_NAME` / `AEGIS_NODE_NAME` (added to `apps/staging/aegis-gateway/rollout.yaml`). Design constraints, all preserved: records with no valid span context omit the trace fields entirely (no all-zero IDs polluting the Loki index), and pod/node are omitted when their env var is empty (the correct Local-mode posture). `cmd/gateway/main.go` installs the trace-aware logger immediately after `tracing.Init` so spans already exist on contexts; a bootstrap logger covers any pre-tracing startup error.

No new ADR is warranted — this is the logging-side wiring of the same observability contract this ADR and ADR-0005 R4 already established, not a new systemic decision. Engine-side log↔trace correlation rides on the deferred opentelemetry-cpp slice (same gate as the engine OTLP exporter).

## Context

Until this slice, neither service exposed metrics. Two shapes of "add Prometheus" were on the table:

1. **In-process pull endpoint** (chosen) — each service runs a small HTTP server on a dedicated port that Prometheus scrapes. Canonical Prometheus pattern; zero external dependency beyond the scraper ldz already installed.
2. **OTLP metrics push via OpenTelemetry Collector** — push-model, routes through an intermediary. Strictly better for federation + multi-backend (fan out to Prometheus + Datadog + CloudWatch simultaneously), but requires running the Collector alongside.

ldz #46 documented the cluster's scrape-first posture (kube-prometheus-stack, no OTEL Collector today), so pull is the right fit for C-Obs-1. Migration to push-based OTLP is tracked as a Phase 4d candidate; ADR-0033 stays pull-scoped.

## Decision

**Both services expose a Prometheus pull endpoint on `:8081` by default, carrying 4 baseline metrics each, scraped by `ServiceMonitor` resources discovered by ldz's kube-prometheus-stack Operator.**

### Port convention

`:8081`, named `metrics` on both Service + Deployment port specs. Per ldz #46 §"Q3" walkthrough:

- **Rejected `:9100`**: collides visually with `node-exporter`'s conventional port; operator-cognition cost on every `kubectl port-forward` and dashboard inspection
- **Rejected `:2112`**: client_golang default; Go-biased convention that imposes awkward symmetry on the C++ engine
- **Accepted `:8081`**: K8s controller-runtime mgmt-port convention — companion to `:8080` main HTTP on the gateway; same port on engine keeps the `ServiceMonitor` template single-pattern (named port reference, not literal)

ServiceMonitor references the port **by name** (`port: metrics`) so a port-number change in the future is a single-manifest edit without breaking scrape contract.

### Four baseline metrics per service

**Engine** (C++, `engine_cpp/src/metrics/`):

- `aegis_engine_up` (Gauge) — 1 after gRPC listener binds
- `aegis_engine_model_loaded{model}` (Gauge) — 1 per registered model
- `aegis_engine_rpc_total{method, status}` (Counter)
- `aegis_engine_rpc_duration_seconds{method}` (Histogram)

**Gateway** (Go, `gateway_go/internal/metrics/`):

- `aegis_gateway_up` (Gauge) — 1 after all three listeners bind (HTTP :8080, gRPC :9090, metrics :8081)
- `aegis_gateway_active_sessions` (Gauge) — point-in-time polled from `session.Registry.Len()` every 5s
- `aegis_gateway_rpc_total{method, status}` (Counter)
- `aegis_gateway_rpc_duration_seconds{method}` (Histogram)

**Cardinality bounds**: `method` is a closed enum (RPC method names from the `.proto`); `status` is coarse `"ok" | "error"` — fine-grained gRPC status codes (`UNAVAILABLE`, `DEADLINE_EXCEEDED`, etc.) are one-line additions if operators ask. `model` is bounded by ADR-0026's manifest (single-digit entries). Upper-bound time-series count per service: ~40, well below Prometheus cost thresholds.

**Histogram buckets** identical across both services (`{0.001, 0.01, 0.1, 1.0, 5.0, 30.0, 120.0, 600.0}` seconds) so Prometheus aggregation queries (`rate(*_rpc_duration_seconds_bucket)`) compose across services without bucket-realignment.

### Implementation

**Engine** — prometheus-cpp via Bazel Central Registry (`bazel_dep(name = "prometheus-cpp", version = "1.3.0.bcr.2")`). **Happy surprise**: prometheus-cpp lives on BCR, so no `rules_foreign_cc` CMake wrapper needed — unlike whisper.cpp / llama.cpp / libopus which do. Cuts integration cost from ~half-day to 10 minutes.

HTTP pull server via `prometheus::Exposer` on `:8081`, running civetweb in its own thread pool. RAII-managed (unique_ptr); destructor stops the server on orderly shutdown. Per-RPC instrumentation via an `RpcInstrument` RAII helper at handler entry — avoids the heavier `grpc::experimental::ServerInterceptor` interface for first cut.

**Gateway** — `github.com/prometheus/client_golang` v1.20.5 pulled through `gateway_go/go.mod` → gazelle `go_deps` → MODULE.bazel `use_repo`. Third `http.Server` alongside the existing `:8080` HTTP and `:9090` gRPC. gRPC instrumentation via `grpc.ChainUnaryInterceptor` + `grpc.ChainStreamInterceptor` (auth interceptor + metrics interceptor) — chained so metrics sees the final handler error.

### LOCAL mode opt-out

Both services default metrics **on**; `AEGIS_ENGINE_METRICS_ADDR` and `AEGIS_GATEWAY_METRICS_ADDR` override. **Explicit empty string = disabled.**

`cmd/app_local` sets `AEGIS_ENGINE_METRICS_ADDR=""` on the engine child so it doesn't collide with the gateway's `:8081` on the same host. Gateway's `:8081` stays on in LOCAL (nothing to scrape it, but harmless and lets a curious dev `curl localhost:8081/metrics`). If a dev wants both off, they set both envs to empty explicitly.

Rationale for default-on: debug ergonomics. Running `bazel run //engine_cpp/cmd/engine:engine` standalone lets a dev `curl localhost:8081/metrics` to inspect state — the common case. Port collision is the special case, handled explicitly.

### ServiceMonitor discovery

Per ldz #46 contract (wide-open selectors), `ServiceMonitor` in the `aegis` namespace with `app.kubernetes.io/part-of: aegis-core` label auto-discovers. `endpoints[].port: metrics` — **name-reference, not numeric** — so future port changes cascade through the Service spec, not the ServiceMonitor spec.

NetworkPolicy adds an ingress allow-rule for the `monitoring` namespace (where kube-prometheus-stack runs) to reach `:8081`. Scrape traffic is plaintext — metrics don't carry PII under our current label taxonomy, and the `monitoring` namespace is inside the cluster trust boundary per ldz #54.

## Alternatives Considered

### A. prometheus-cpp via rules_foreign_cc CMake wrapper

**Not needed.** Would have been the natural fallback if BCR didn't carry prometheus-cpp. `rules_foreign_cc` wraps CMake-native builds (whisper, llama, libopus all use this pattern); one-time integration cost is ~half a day and adds to the third-party build surface the engine's linker already carries. BCR availability made this obsolete; noted here only to close the question for future readers who might wonder why prometheus-cpp doesn't live under `engine_cpp/third_party/`.

### B. OpenTelemetry Collector + OTLP push

**Deferred to Phase 4d consideration.** Push model scales better for multi-backend fan-out (Prometheus + Datadog + CloudWatch) and removes the "each service runs an HTTP server" surface. But it requires running the Collector as a sidecar or DaemonSet, which ldz doesn't have today (#46 is explicitly scrape-centric). Re-open when ldz ships an OpenTelemetry Collector — trigger is "ldz adds Collector to platform contract"; aegis-core then writes ADR-0034 evaluating OTLP-push vs keep-pull.

### C. gRPC interceptor-based instrumentation (engine)

**Deferred.** The grpc-cpp `experimental::ServerInterceptor` interface is more ceremony than value for a 2-method service. Handler-entry RAII instrumentation gives the same numbers with less code. If the engine gains many more gRPC methods and the duplication becomes visible, revisit — this is the engine-side equivalent of "prefer middleware over per-handler code when the chain is deep."

### D. Per-code status labels instead of coarse ok/error

**Deferred.** Starting coarse bounds cardinality and matches the engine's parallel choice. Operators can escalate to `{status_code="UNAVAILABLE"}` breakdown via a one-line label-expansion change (`status.Code(err).String()` in Go; `grpc::StatusCode` switch in C++). No premature expansion today.

### E. Include histogram buckets for request size / response size

**Not in C-Obs-1 scope.** Duration is the load-bearing latency signal for SLO gates; sizes are diagnostic, not alert-driving. Addable in a follow-up if operators want bandwidth shape analysis.

## Consequences

### Positive

- Phase 4d SLO-gate canary (C-5b, per ADR-0030 §"Honest phasing") has a metric source as soon as the `AnalysisTemplate` wiring lands.
- Rich per-service telemetry with modest implementation footprint: ~300 LoC across both services + 2 ServiceMonitor manifests.
- Default-on stance matches operator expectations; opt-out is explicit, not accidental.
- Portfolio signal: "Phase 4c implementation slice with observable outcome" — paired with ADR-0030/0031/0032 gives a full arc of `(design decision ADR) → (cross-repo coordination) → (implementation slice) → (design decision ADR)`.

### Negative

- Metrics code lives in two languages; keeping metric names + label schemes in sync across services is a manual discipline (no code-generator binding). Mitigated by the naming convention (`aegis_{engine,gateway}_*`) making divergences visible.
- Coarse `ok/error` status labels lose per-error-code granularity. Acceptable for first cut; documented revisit trigger.
- `civetweb` transitively enters the engine's link line via prometheus-cpp's pull library. Adds some HTTP-server surface to the C++ binary; upstream is well-maintained (12+ year track record, no CVE in prometheus-cpp's bundled version).

### Neutral

- HTTP pull endpoint is independent of the mTLS path; metrics stay plaintext. When Phase 4c C-2 wires cert-manager for the gateway↔engine mTLS, the `:8081` port is deliberately NOT under mTLS — it's a separate listener with its own (plaintext) policy. Operators querying `/metrics` from the `monitoring` namespace don't need certs.

## Triggers to revisit

1. **OpenTelemetry Collector lands on ldz** → ADR-0034 evaluates OTLP-push migration
2. **Service count grows beyond 2** → richer metric-registry organization (per-service sub-registries, name-prefix enforcement at registration time)
3. **SLO-burn-rate analysis shows bucket resolution insufficient** at the extremes (unary handlers: 1ms too coarse, or streams: 600s too coarse) → split histograms per-method or refine bucket boundaries
4. **Multi-region / multi-cluster** deploy → metric cardinality implications from `cluster` label, Prometheus federation vs Thanos evaluation

## Implementation checklist

- [x] `MODULE.bazel` adds `bazel_dep(name = "prometheus-cpp", ...)`
- [x] `engine_cpp/src/metrics/` package — Registry + family accessors + buckets
- [x] `engine_cpp/src/grpc/aegis_engine_service.cc` — `RpcInstrument` RAII instrumentation on `StreamTranscribe` + `Health`
- [x] `engine_cpp/cmd/engine/main.cc` — `prometheus::Exposer` on :8081, env opt-out, `Up` + `ModelLoaded` initial gauges
- [x] `gateway_go/internal/metrics/` package — Registry + 4 metrics + interceptors + handler
- [x] `gateway_go/cmd/gateway/main.go` — third `http.Server` on :8081, `ChainUnaryInterceptor` / `ChainStreamInterceptor` wire, env opt-out, active-sessions polling goroutine
- [x] `gateway_go/cmd/app_local/main.go` — `AEGIS_ENGINE_METRICS_ADDR=""` on engine child to avoid port collision
- [x] `apps/staging/aegis-{gateway,engine}/servicemonitor.yaml`
- [x] `apps/staging/aegis-{gateway,engine}/service.yaml` — named `metrics` port
- [x] `apps/staging/aegis-{gateway,engine}/deployment.yaml` — `containerPort: 8081` named metrics
- [x] `apps/staging/aegis-{gateway,engine}/networkpolicy.yaml` — ingress allow from `monitoring` ns on :8081
