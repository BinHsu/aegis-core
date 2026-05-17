# ADR-0031: mTLS Without Service Mesh — cert-manager + gRPC-native TLS

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-20                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4c must realize ARCH §"Zero Trust Networking (mTLS)" (line 180), which reads: *"the communication between the Go Gateway and the C++ Engine is protected by a Service Mesh (e.g., Istio) enforcing Mutual TLS."* The `e.g.` is load-bearing — this ADR formalizes the mTLS commitment while rejecting the mesh path and writing down why. |
| Related  | ADR-0017 (Gateway–Engine topology), ADR-0030 (Argo Rollouts — also no-mesh), ADR-0028 (Cosign keyless — same `cert-manager + Fulcio` PKI heritage), ARCH §10.6 (Enterprise-Grade Compliance), `aegis-aws-landing-zone#54` (platform surface contract — mTLS primitives) |
| Supersedes | ARCH §"Zero Trust Networking" mesh-by-default implication (line 180). ARCH's `e.g., Istio` is preserved as an example of the family of solutions, but the binding mechanism for Aegis is documented here. |

## Context

ARCH §10.6 commits the project to mTLS between the Go gateway and the C++ engine. The literal reading of line 180 names Istio as the canonical realization; the wider reading (`e.g.`) leaves the delivery mechanism open. This ADR closes the mechanism.

The mTLS commitment itself is **non-negotiable and preserved**:
- Workload identity verified on both sides of every gateway↔engine RPC
- All in-cluster traffic between these services encrypted
- Certificate rotation automatic; no manual cert handling in production

What this ADR decides is **how to deliver mTLS without adopting a service mesh**, and why that's the right call for Aegis-scale.

### The scale reality check

Aegis in Phase 4c has **two in-cluster services** (gateway, engine), talking to **one external dependency cluster-internally** (qdrant — but that's over gRPC with its own auth, not mTLS scope). ARCH §10.6 writes the mTLS commitment in the shape of a 50-service microservices estate; we have 2.

Service mesh's design payoff scales roughly with `O(service-count × interaction-frequency)`. At `n=2`, the payoff is `4 interaction pairs` worth of benefit against a fixed control-plane + sidecar tax. The ratio is unfavourable; documenting this openly is stronger than quietly inheriting a mesh "because the reference architecture said so."

## Decision

**Deliver mTLS via cert-manager-issued per-workload certificates + gRPC-native TLS with in-process rotation, for Phase 4c onward. No service mesh, no data-plane proxy, no sidecar.**

### Mechanism

1. **PKI root** — cert-manager `ClusterIssuer` using a private CA rooted in AWS Private CA (Phase 4c+ ldz-provisioned) OR a cert-manager-managed self-signed intermediate for Phase 4c staging. The Phase 4c-tier decision is intentionally the lightweight self-signed intermediate; migrating to AWS Private CA is a Certificate-CR backend swap, no application change.

2. **Per-workload identity** — each Deployment's pods mount a `Certificate` CR output Secret:
    - `aegis-core-gateway.aegis.svc.cluster.local` + `aegis-core-gateway.aegis.svc` + `aegis-core-gateway.aegis`
    - `aegis-core-engine.aegis.svc.cluster.local` + `aegis-core-engine.aegis.svc` + `aegis-core-engine.aegis`
    - Kubernetes DNS names in SANs; K8s `ServiceAccount` name carried in the CN for audit correlation

3. **Rotation** — cert-manager renews certs automatically at 2/3 of their lifetime (default 90d cert → renew at 60d). The underlying `Secret` updates in place. Workloads read from mounted files via:
    - **Go gateway**: `tls.Config` with `GetCertificate` callback calling a `fsnotify`-based cert loader cached in-memory; reload on `SIGHUP` or inotify event. Stdlib + `github.com/fsnotify/fsnotify` only.
    - **C++ engine**: gRPC-C++'s built-in `grpc::experimental::FileWatcherCertificateProvider` (stable since gRPC 1.41, C++ API at `grpcpp/security/tls_credentials_options.h`). Zero custom rotation code.

4. **Enforcement** — gateway dials engine with `credentials.NewTLS` + explicit `ServerName` + `RootCAs` pointing at the CA cert. Engine server requires client certificates (`grpc::SslServerCredentialsOptions{.client_certificate_request = RequestAndRequireClientCertificateAndVerify}`). **Unauthenticated connection → hard reject at handshake.**

5. **Namespace policy (defense in depth)** — Kyverno / Gatekeeper ClusterPolicy (Phase 4c C-6 slice) denies pods in the `aegis` namespace that lack the TLS cert volume mount. Belt-and-suspenders for the cert-mount invariant.

### What mTLS covers vs what it doesn't

mTLS covers **in-cluster gateway↔engine** traffic confidentiality + workload authentication. Out of scope for this ADR (and correctly handled elsewhere):
- **Ingress TLS** (client → gateway): ALB-terminated TLS with ACM cert (ldz territory). Re-encryption from ALB target-group to gateway pod is a separate decision; current posture is ALB-to-pod is in-VPC private subnet, encryption optional for staging, required pre-prod.
- **Gateway auth/authz** (who is the end user?): Cognito + JWT per ARCH §10.6; orthogonal to mTLS.
- **Engine egress to qdrant**: qdrant's own TLS + API-key auth; not mTLS scope.

Calling this out explicitly prevents the "mTLS solves auth" conflation that mesh marketing sometimes invites.

### LOCAL mode posture — plaintext on localhost, by design

The CLOUD mode mechanism above is meaningless in LOCAL mode. Physical reality:

- `bazel run //:app_local` starts the Go gateway as the parent process, which spawns the C++ engine as a child via `exec.Command` (ARCH §5 "Process Supervisor Pattern"). Both processes run on the same host, same user, same trust domain.
- gRPC communication is over localhost (TCP loopback today; unix socket remains a future option per ADR-0017's topology table).
- No Kubernetes, no cert-manager, no `ClusterIssuer`, no Secret volumes. The cert-manager mechanism literally cannot run.

**Decision for LOCAL mode: gRPC plaintext on localhost; no TLS, no mTLS.** The trust boundary in LOCAL mode is the host itself — an attacker who compromises localhost has already bypassed anything TLS could have defended (they can `ptrace`, they can read process memory, they can modify the binary on disk). Adding a self-signed cert would be pure ceremony without a defended threat model.

This matches the pattern ARCH §8 "The Local Mode Interface Fallback" line 186-189 establishes for the other enterprise components (Cognito → dummy token, External Secrets → `.env` file). mTLS joins that list:

| Enterprise component | CLOUD mode     | LOCAL mode                                   |
| -------------------- | -------------- | -------------------------------------------- |
| User auth            | Cognito JWT    | dummy local token authenticator              |
| AWS credentials      | EKS Pod Identity | implicit via local AWS CLI profile / none  |
| External secrets     | AWS Secrets Manager + External Secrets Operator | `.env` file in bazel sandbox |
| Workload mTLS        | cert-manager + gRPC TLS (this ADR) | **plaintext gRPC over localhost** |

**Interface injection point** — the gRPC dial + listen plumbing is abstracted behind a credentials factory:
- Gateway side: `func DialEngine(addr string) (*grpc.ClientConn, error)` — CLOUD mode factory returns `credentials.NewTLS(tlsConfig)` with file-watcher reload; LOCAL mode factory returns `insecure.NewCredentials()`. Wired at process start via `DeployMode` env var.
- Engine side: `grpc::ServerCredentials*` — CLOUD returns `grpc::experimental::TlsServerCredentials(...)` with `FileWatcherCertificateProvider`; LOCAL returns `grpc::InsecureServerCredentials()`.

Application RPC code is identical in both modes; the difference lives entirely in the credentials injection.

**Trigger to reconsider LOCAL plaintext:**

1. LOCAL mode's audience changes from "solo dev / offline demo" to "multi-tenant host" (e.g., shared dev VM where two users could see each other's localhost traffic). No such pivot is planned.
2. A compliance regime (e.g., FIPS 140-2) demands TLS-in-transit regardless of trust boundary, with no "on-host" exemption. Currently out of scope.

Until then, LOCAL's plaintext posture is a documented deliberate choice, not an oversight.

## Alternatives Considered

### A. Istio (classic sidecar mode)

**Rejected.** Honest cost accounting:

| Resource                | Istio classic cost                                                  |
| ----------------------- | ------------------------------------------------------------------- |
| Control plane (`istiod`) | ~500MB RAM + 0.5 CPU baseline                                       |
| Per-pod sidecar (Envoy) | ~50MB RAM + ~10m CPU idle, more under load                          |
| Cert rotation           | ✅ Automatic via Citadel-issued SPIFFE SVIDs                         |
| Observability           | ✅ Mesh-wide telemetry out of the box                                |
| Debugging cost          | ⚠️ High — `istioctl proxy-config`, Envoy config inspection, sidecar logs stream |
| Learning cost           | ⚠️ VirtualService / DestinationRule / PeerAuthentication / AuthorizationPolicy — four CRDs to reason about for what is fundamentally "turn on mTLS" |
| Upgrade blast radius    | ⚠️ Control-plane upgrades can break data-plane compatibility; rolling Envoy restart churns traffic |

For **2 workloads**, the per-pod sidecar tax alone exceeds the memory the actual services consume for their primary work. The mesh's real payoff (rich traffic policy, cross-service observability) is unrealized at this scale.

**Trigger to revisit: Istio adoption for other reasons (multi-cluster, fleet-wide observability, advanced traffic policy for >10 services). When that trigger fires, this ADR is re-opened and mTLS likely moves to mesh-provided.**

### B. Istio Ambient Mode (sidecarless, 2024+)

**Rejected for Phase 4c; preserved as Phase 5 candidate.**

Ambient mesh removes the per-pod sidecar and replaces it with per-node ztunnels + optional per-namespace L7 waypoint proxies. This materially reduces the per-pod tax that sinks classic Istio at our scale — arguably, ambient is the only Istio variant Aegis should ever entertain.

But Phase 4c is too early:
- Ambient mesh GA was recent; production-readiness signals from the community are still maturing
- Debugging tooling (`istioctl` variants for ambient) still catching up to classic
- For 2 workloads, even ambient's reduced tax isn't clearly worth the ops surface

**Phase 5 placeholder on ROADMAP**: if mesh-native observability / multi-cluster becomes a real need, ambient Istio is the first thing evaluated, not classic.

### C. Linkerd

**Rejected for Phase 4c.** Linkerd is **the best-engineered mesh** by most reasonable metrics — Rust data plane, predictable latency tax (~1ms), tight CNCF focus, minimal CRD surface. For an Aegis 2x the size, this would be a closer call.

At 2 workloads:
- Still sidecar-per-pod (pre-ambient Linkerd), so the `n=2` math still tilts against
- Linkerd's `ServiceProfile` identity model is SPIFFE-based, which is stronger than cert-manager's CA-based but overkill when K8s ServiceAccount+RBAC already provides the coarse-grained identity we need
- Ecosystem fit: less argoproj-adjacent than some alternatives; community overlap with our ArgoCD-centric deploy is moderate

**Trigger to revisit: multi-cluster + latency-sensitive mesh need. Linkerd becomes the strongest candidate specifically for those.**

### D. SPIFFE SPIRE (identity without mesh)

**Rejected for Phase 4c; acknowledged as the upgrade path if identity needs intensify.**

SPIFFE SPIRE provides **workload identity via attestation** — each workload proves it's running in a particular K8s pod / service account / node + gets issued a short-lived X.509 SVID. This is strictly stronger than cert-manager's CA-based identity, because:
- cert-manager: "who has a cert signed by this CA" → if the cert's private key leaks, anyone can impersonate
- SPIFFE SPIRE: "who the workload-attestor agrees is running as this workload" → compromise is harder; rotation is minutes, not days

Cost:
- SPIRE server (1 pod, ~100MB RAM, needs datastore — PostgreSQL or SQLite PVC)
- SPIRE agent DaemonSet (1 pod per node, ~30MB RAM each)
- Application integration via SPIFFE Workload API (`github.com/spiffe/go-spiffe/v2` for Go; `github.com/spiffe/cpp-spiffe` for C++)

For **2 workloads**, the SPIRE ops surface (cert authorities, node attestation policies, registration entries) is non-trivial. cert-manager is "install controller, write YAML"; SPIRE is "understand the attestation model, write selectors, debug when attestation fails."

**Trigger to revisit: > 5 in-cluster services OR multi-cluster identity federation OR compliance regime demanding attestation-grade identity (e.g., FedRAMP High, some financial-sector frames).**

### E. ALB mutual TLS (client-cert at edge)

**Not applicable.** ALB mTLS verifies *client* certificates at the ingress edge — it's about browser/client-side cert presentation, not internal service-to-service identity. Does not substitute for gateway↔engine mTLS.

### F. No mTLS, rely on NetworkPolicy + gRPC plaintext

**Rejected.** NetworkPolicy enforces L3/L4 reachability only. It prevents "engine pod in `other-namespace` can reach gateway" but does nothing against:
- A compromised pod inside the `aegis` namespace
- A node-level compromise that can inject into pod network namespace
- Post-handshake eavesdropping on the wire

ARCH §10.6 mTLS commitment is incompatible with plaintext; this is not on the table.

## Comparison summary

| Dimension                     | cert-manager + gRPC TLS (picked) | Istio classic      | Istio ambient   | Linkerd          | SPIFFE SPIRE         |
| ----------------------------- | -------------------------------- | ------------------ | --------------- | ---------------- | -------------------- |
| Baseline memory overhead      | ~50MB controller                 | ~500MB + sidecars  | ~200MB + ztunnels | ~200MB + sidecars | ~100MB server + agents |
| Per-pod overhead              | ≈0 (file mount)                  | ~50MB sidecar      | ≈0 (ztunnel offload) | ~30MB sidecar   | ≈0 (SDK call)        |
| Identity model                | CA-signed cert                   | SPIFFE via Citadel | SPIFFE via ztunnel | SPIFFE           | SPIFFE (native)      |
| Cert rotation                 | cert-manager + file watcher      | Automatic          | Automatic       | Automatic        | Automatic (minutes)  |
| New CRDs to learn             | 1 (`Certificate`)                | 4+                 | 4+              | 2                | 3+                   |
| Application code changes      | gRPC TLS config + fsnotify       | None (transparent) | None            | None             | Workload API calls   |
| Breaks if mesh control-plane down | Partial (new certs blocked) | Hard (no traffic)  | Hard            | Hard             | Partial (new SVIDs blocked) |
| Fit for `n=2` workloads       | Excellent                        | Severe overkill    | Still heavy     | Heavy            | Overkill             |

The dominant signal: **at n=2, cert-manager trades a modest application integration cost for massive reductions in control-plane ops surface**. As `n` grows, mesh solutions close the gap and eventually win.

## Consequences

### Positive

- Phase 4c can ship mTLS in one PR (ADR-0031 → cert-manager CR + TLS config) without waiting on mesh install.
- No sidecar tax; pod resource budgets stay simple to reason about.
- Control-plane scope limited to cert-manager, which is already widely deployed (and will be in ldz for ACM / ingress TLS anyway — zero marginal install cost).
- ARCH §10.6 commitment met honestly: mTLS is real, certs rotate, application verifies.
- Portfolio signal: **rejecting an over-scoped tool with a written ADR is a stronger architect signal than adopting it**. See §"ADR as the muscle flex" in the session close-note narrative on this pattern (Phase 4a EXIT debrief).
- Upgrade path preserved: SPIFFE is a cert-manager-Compatible upgrade (both issue X.509); Istio ambient is a sidecarless add-on; neither requires a ground-up rewrite.

### Negative

- Application code carries the cert-loading + rotation-reload plumbing. Roughly 50 LoC each side (Go + C++). Cost is one-time; mTLS mesh hides this cost but pays a worse recurring cost elsewhere.
- No mesh-native observability for gateway↔engine (tracing, metrics). Phase 4d's OTLP pipeline must cover application-layer observability; mesh would have given some of this "free" but we've chosen to pay it in application code.
- Debugging mTLS handshake failures falls on the app developer (TLS error messages in gateway + engine logs), not a mesh CLI. Initially painful; solved by adding structured logging of TLS handshake outcomes (one-line add per side).
- Explicitly diverges from ARCH's `e.g., Istio` hint. Future contributors will need to read this ADR to understand the non-mesh posture. ARCH will be updated with a forward reference to ADR-0031 as part of the Phase 4c ADR-write slice.

### Neutral

- Does not preclude adopting SPIFFE, Istio ambient, or Linkerd later; triggers are written down above.
- cert-manager ClusterIssuer swap (self-signed → AWS Private CA → Vault PKI → whatever) is a backend change; application code unchanged.

## Triggers to re-open this decision

Summarized from alternatives above:

1. **In-cluster service count > 5** — mesh `O(n)` payoff starts beating cert-manager ops simplicity.
2. **Multi-cluster federation** — cross-cluster identity is SPIFFE's sweet spot; cert-manager handles poorly.
3. **Compliance regime requires attestation-grade identity** (FedRAMP High, PCI DSS L1, some financial frames). Move to SPIFFE SPIRE first; mesh second.
4. **Mesh-native observability becomes a real need** — debugging in a 10+ service topology is materially easier with a mesh. Ambient Istio or Linkerd become candidates.
5. **Zero-trust audit** demanding "no long-lived credentials anywhere" — cert-manager's ~60-day certs fall short; SPIFFE's minutes-scale SVIDs pass.

None of these are in the Phase 4c / 4d ROADMAP today.

## Implementation checklist (tracked in ROADMAP Phase 4c)

- [ ] Install cert-manager to `cert-manager` namespace on ldz EKS (likely already planned by ldz for ACM/ingress — coordinate via cross-repo issue)
- [ ] Create `ClusterIssuer` — self-signed intermediate for Phase 4c staging
- [ ] Write `apps/staging/aegis-core-gateway/certificate.yaml` + `apps/staging/aegis-core-engine/certificate.yaml`
- [ ] Go gateway: TLS loader with fsnotify-based reload (`gateway_go/internal/tlsreload/`)
- [ ] C++ engine: `FileWatcherCertificateProvider` wiring (`engine_cpp/src/grpc/`)
- [ ] Gateway dial config updated to require TLS + verify engine cert SAN
- [ ] Engine server config updated to require client cert (mTLS, not just TLS)
- [ ] Structured logging for TLS handshake outcomes both sides
- [ ] Kyverno ClusterPolicy denying pods in `aegis` namespace without cert volume mount (C-6 slice)
- [ ] ARCH §10.6 updated with forward reference to this ADR

## Cost summary

- **Per-cluster overhead**: cert-manager controller ~50MB RAM (likely already installed for ACM)
- **Per-pod overhead**: ~0 (file-mounted Secret + application-level TLS)
- **Application code**: ~50 LoC Go + ~30 LoC C++ (file loader + reload hooks), one-time
- **Ongoing operational complexity**: low — cert-manager is a mature, single-controller dependency
- **Migration cost** if we later adopt mesh: low — cert-manager Certificate objects coexist with SPIFFE-issued SVIDs; application TLS code is replaced by mesh-transparent TLS termination
