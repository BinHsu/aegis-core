# ADR-0017: Gateway–Engine Topology — N:N-ready by design, realized by deployment

| Field    | Value                                                                   |
| -------- | ----------------------------------------------------------------------- |
| Status   | Accepted                                                                |
| Date     | 2026-04-14                                                              |
| Deciders | Project author                                                          |
| Context  | Phase 3 scoping; prompted by the Opus-decode-on-engine refactor (ADR-0016) surfacing "is the gateway-engine connection a pool?". |
| Related  | ADR-0004 (no shared state between replicas), ADR-0006 (keepalive + drain), ADR-0007 (local vs cloud), ADR-0013 (proto distribution) |

## Context

As of Phase 2, the Aegis Core gateway dials exactly one engine
(`grpc.NewClient(engineAddr, …)` in `gateway_go/cmd/gateway/main.go`);
the Local-mode launcher (`bazel run //:app_local`) starts exactly one
of each as subprocesses on a single host.

Phase 4+ Cloud-mode deployment (per the contract on
[`aegis-aws-landing-zone#54`](https://github.com/BinHsu/aegis-aws-landing-zone/issues/54))
will run both behind EKS. The question: **is the application code
obligated to know the cluster topology, or is topology purely a
deploy-time concern?**

Four topology modes are relevant:

| Mode  | When used                                        |
| ----- | ------------------------------------------------ |
| 1:1   | Local mode (host laptop); Phase 2 single-process demo |
| 1:N   | Cloud MVP: one gateway pod talks to a pool of engine pods for load-spreading |
| M:1   | Almost never — gateway is cheap, engine is expensive; adding gateways without adding engines starves on compute |
| M:N   | Production scale — independent horizontal scaling of both tiers |

The engine is stateful per-session (whisper context + PCM ring
buffer + Opus decoder). Once a `StreamTranscribe` stream is open
for a meeting, the meeting is pinned to that engine for its
lifetime; no in-flight migration. The gateway is stateful per-
replica (session registry, fan-out channels); ADR-0004 locked
in "no shared state between gateway replicas."

## Decision

**Gateway and Engine code is N:N-ready from Phase 2 onward.
Deployment topology (which mode above) is chosen at the deploy-
time layer — Kubernetes Service shape, ALB session affinity,
Karpenter node class selection — not at the application-code
layer.**

### What this means in code

**Gateway (`gateway_go/`):**

- Engine dial uses gRPC's built-in client-side load-balancing.
  `AEGIS_ENGINE_ADDR` can be any resolver-understandable target:
  - `localhost:50051` — single engine (Phase 2 local).
  - `dns:///engine.aegis.svc.cluster.local:50051` — Kubernetes
    Headless Service resolving to N pod IPs; gRPC round-robin
    picks one per new `StreamTranscribe` stream (Phase 4+ cloud).
  Code always registers `"loadBalancingConfig":[{"round_robin":{}}]`
  as the default service config — a single-resolution target
  is a no-op; a multi-resolution target auto-balances.
- Session state stays per-replica in memory (ADR-0004 invariant):
  registry, fan-out, per-session stop functions, WebRTC
  negotiator. Never synchronize across gateway replicas;
  never cache cross-session state that would need eventual
  consistency.
- Keepalive is per-connection (ADR-0006), not per-cluster. Adding
  pods does not break either side's clocks.
- No gateway-to-gateway communication. Viewer session affinity
  (boss scans QR → always lands on the same gateway as the host)
  is the load balancer's job (sticky routing on `session_id` path
  segment or query parameter), NOT the application's.

**Engine (`engine_cpp/`):**

- Each `StreamTranscribe` bidi stream owns its own session state:
  whisper context, Opus decoder (ADR-0016), PCM ring buffer,
  control-state machine. No cross-stream coordination.
- Engine does not — and must not — know how many gateways are
  in the fleet. It accepts streams; it owns per-stream state; it
  returns transcripts.
- `ResourceBudget` (ADR-0010) enforces fail-fast on memory
  pressure, keeping failure blast radius to the over-budget
  session — other sessions on the same engine continue.

**Proto (`proto/aegis/v1/aegis.proto`):**

- No topology-bearing fields. Session identity is the
  `session_id`, not a physical target. gRPC stream semantics
  provide automatic session-to-engine pinning; no routing
  hints in the proto layer.

### What deployment realizes

Deployment is where topology is actually chosen. The infra side
(`aegis-aws-landing-zone`) configures:

- **Engine tier**: Kubernetes Deployment with N replicas (N ≥ 1).
  Exposed via a Headless Service that gRPC's DNS resolver expands
  to N pod IPs. Karpenter provisions whatever compute profile
  (CPU or GPU) the engine needs.
- **Gateway tier**: Kubernetes Deployment with M replicas (M ≥ 1).
  Exposed via an ALB with **session affinity** keyed on
  `session_id` (the `/view/:sessionId` URL segment or the
  `x-session-id` header — exact mechanism is the landing-zone
  side's design choice).
- **Local mode**: still 1:1 via `bazel run //:app_local`.
  Cloud-mode config is off.

### Cross-repo coordination

A standing cross-repo issue on `aegis-aws-landing-zone` captures
the infra-side configuration needed for M:N:

- Headless Service DNS format for the engine pool (used by
  gateway's `AEGIS_ENGINE_ADDR`).
- ALB session-affinity rule for the gateway pool (viewer
  requests must reach the same replica as the host's
  CreateMeeting RPC).
- Keepalive-friendly timeouts at the LB layer (no premature
  idle-timeout killing long-lived transcript streams per
  ADR-0006).

## Consequences

### Positive

- **One-line code change suffices** to move from 1:1 demo
  deployment to M:N production. No gateway refactor, no engine
  refactor, no proto evolution at that transition.
- **ADR-0004 + ADR-0006 + proto contract + this decision**
  compose into a clean separation: application code is topology-
  oblivious; deployment layer is topology-specific. Standard
  cloud-native pattern (Envoy / Istio / ALB session affinity).
- **Failure blast radius stays tight**: one engine crash kills
  one meeting's worth of sessions (the sessions pinned to that
  engine), not the whole fleet. Surviving engines continue.
- **Portfolio signal**: "designed for the scale shape we don't
  currently need, at zero marginal cost" — architect-grade
  restraint.

### Negative / costs

- `WithDefaultServiceConfig(round_robin)` in the dial is a
  small configuration that someone skimming Phase 2 code will
  ask "why do we need this for a single engine?" — resolved by
  the inline comment pointing at this ADR.
- Developers must resist the "just hardcode the engine URL here
  for now" reflex in PRs. The memory entry
  `feedback_topology.md` exists specifically to flag this in
  review.
- Session affinity complexity sits in the landing-zone repo,
  which means cross-repo coordination is non-trivial. The
  paired standing issues (`aegis-core#11` ↔
  `aegis-aws-landing-zone#54`) are the coordination mechanism;
  this ADR should land alongside a `cross-repo/fyi` notice on
  `aegis-aws-landing-zone`.

## Alternatives Considered

### A. Assume 1:1 for now, refactor to N:N when production load justifies it

- **Pros**: Simpler-looking code in Phase 2 reviews. No "why is
  this LB config here?" questions.
- **Cons**: Creates a future refactor that touches dial logic,
  tests, ADRs, possibly proto comments. Refactor-timing is also
  a trap: "when load justifies it" usually means "when we're
  already on fire." Whereas the N:N-ready form is a one-liner
  that costs nothing at Phase 2 stakes.
- **Rejected** because the cost of staying N:N-ready from day
  one is strictly less than the cost of closing the gap under
  load.

### B. Full-mesh coordination between gateway replicas (e.g. Raft / gossip for session directory)

- **Pros**: Any viewer can land on any gateway and be routed to
  the right session.
- **Cons**: Introduces a distributed-consensus dependency (etcd,
  a gossip library). Massive complexity for a feature that
  Phase 2 doesn't need — ADR-0004 explicitly chose the
  stateless-relay model to avoid this.
- **Rejected** as gross over-engineering. Sticky LB routing is
  the well-trodden alternative.

### C. Add a "session directory" service as a third tier

- **Pros**: Gateway and engine are both stateless; the directory
  tier tracks `session_id → (gateway, engine)` mappings.
- **Cons**: Three tiers of state to manage. Directory becomes a
  single point of failure. ADR-0004 rejected this style of
  coordination specifically.
- **Rejected** for the same reason as (B) — wrong level of
  complexity for the problem.

### D. Deploy N:N from day one, including local mode

- **Pros**: Zero topology drift between local and cloud.
- **Cons**: Forces Kubernetes (or docker-compose) dependency on
  every developer laptop. Breaks the CLAUDE.md Rule 3 promise
  of "clone it, `bazel run`, just works." Recruiter demo
  experience becomes "first install Docker Desktop and configure
  a K8s cluster."
- **Rejected** because local-mode demo simplicity is a hard
  portfolio requirement.

## Implementation checklist

- [x] Memory entry `feedback_topology.md` captures the invariant
      for future agent sessions (filed 2026-04-14).
- [x] MEMORY.md index updated.
- [ ] `gateway_go/cmd/gateway/main.go`: engine dial gets
      `grpc.WithDefaultServiceConfig(round_robin)` with an inline
      comment pointing at this ADR.
- [ ] `ARCHITECTURE.md` adds a cross-reference in the section
      discussing the dual-mode topology.
- [ ] `README.md` ADR index table gets row for ADR-0017.
- [ ] Cross-repo `cross-repo/fyi` issue on
      `aegis-aws-landing-zone` signaling the session-affinity
      requirement (no action yet — informational).
