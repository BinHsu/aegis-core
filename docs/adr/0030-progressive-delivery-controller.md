# ADR-0030: Progressive Delivery Controller — Argo Rollouts, not Flagger

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-20                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4c "Progressive Delivery" opening. ROADMAP lines 328–329 leave the controller choice open ("Argo Rollouts or Flagger"); this ADR closes it. Upstream is ArgoCD on `aegis-aws-landing-zone`'s EKS cluster; there is no service mesh installed today and (per ADR-0031) none is planned. |
| Related  | ADR-0017 (Gateway–Engine topology), ADR-0031 (mTLS without service mesh), ARCH §10.4 (Progressive Delivery), `aegis-aws-landing-zone#96` (Kyverno verify-image — admission-time consumer downstream of canary weights) |

## Context

ARCH §10.4 commits the project to SLO-gated canary deploys with automatic rollback on >25% error-budget burn. The Kubernetes ecosystem offers two serious progressive-delivery controllers: **Argo Rollouts** (argoproj, CNCF graduated) and **Flagger** (fluxcd, CNCF graduated). Both can read Prometheus-style analysis sources, both drive weighted traffic splits, both auto-rollback. The first-order functional overlap is near-total.

The choice that actually matters is **which surrounding stack each one assumes**. That decision is pre-committed by two prior choices that were not made for canary reasons:

1. **ArgoCD is our deploy tool** (ldz EKS cluster, GitOps model) — chosen for reconciliation + audit trail, not for canary.
2. **No service mesh today, and none planned** (ADR-0031 documents the "mTLS without mesh" position) — chosen for operational simplicity, not for canary.

These pre-commitments make Argo Rollouts the path of least friction and Flagger the path of most friction, independently of the canary controllers' own merits.

## Decision

**Use Argo Rollouts as the progressive-delivery controller for Phase 4c onward.** `Rollout` CRD replaces `Deployment` for the gateway and engine; step-based weight canary via the ALB traffic-router plugin (`aws-load-balancer-controller` weighted target groups); `AnalysisTemplate` for SLO gates (wiring deferred to Phase 4d once metric pipeline emits — see §"Honest phasing" below).

### Why Argo Rollouts (four binding reasons)

#### B1. Zero mesh dependency

Flagger's design assumes a **traffic-splitting provider** — Istio, Linkerd, App Mesh, Contour, Gloo, or one of a narrow set of ingress controllers (nginx / skipper / Gateway API + specific implementations). Without one of these, Flagger's canary degrades into ingress weight rewrites that miss the mesh-native features it's built around (header-based routing, mesh-level observability, richer traffic policy).

Argo Rollouts is **traffic-provider-agnostic by design**. It supports: AWS ALB (via `aws-load-balancer-controller` plugin), Istio, Linkerd, SMI, Nginx, Gateway API, Traefik, Apisix, Ambassador, Gloo. For a no-mesh deployment on EKS, the ALB plugin is a first-class integration, not a fallback.

Our stack is ALB + no mesh. Flagger loses its design center; Argo Rollouts hits its design center.

#### B2. ArgoCD ecosystem alignment

Argo Rollouts and ArgoCD are **sibling projects under the same `argoproj` CNCF organization**. They share CRD conventions, CLI family (`argo` / `argo rollouts` / `argocd`), release cadence, and UI. ArgoCD's `Application` resource sees a `Rollout` as a native CRD with a progressive rollout UI panel built in; no extra integration glue is needed.

Flagger lives in the `fluxcd` ecosystem — Flux CD is ArgoCD's direct functional competitor. Mixing them is technically supported (both are just CRD controllers, they don't conflict on the API), but:
- The `HelmRelease` / `Kustomization` patterns Flux users rely on are absent; Flagger's happiest-path docs assume Flux as the deploy tool.
- Bug reports and community knowledge cluster around the ArgoCD/Rollouts vs Flux/Flagger axis. Cross-ecosystem debugging loses the benefit of "I can match my exact stack in a GitHub issue."

Choosing Flagger with ArgoCD is not broken, just swimming against the community current.

#### B3. Traffic-split model fits our ingress posture

AWS ALB weighted target groups are the Phase 4c+ ingress approach (inherited from Phase 4a frontend serving via CloudFront + S3 for the web tier, ALB for the API tier). Argo Rollouts' ALB plugin manipulates `TargetGroupBinding` CRDs (from `aws-load-balancer-controller`) to shift weight between the stable and canary ReplicaSets. This is the same primitive ops teams already debug when troubleshooting ALB routing — no new mental model.

Flagger's ALB support exists but is secondary to its mesh-native paths. The flagship Flagger story is "Istio `VirtualService` weight manipulation" or "Linkerd TrafficSplit". Without those, we're using Flagger at the edge of its design space.

#### B4. Escape hatch is cheap

If this decision turns out wrong, migrating from Argo Rollouts to Flagger (or vice versa) is a **template-level rewrite**, not an application change. The application code (gateway, engine) is canary-oblivious — canary lives entirely in manifest land. Swapping controllers means:
- Replace `kind: Rollout` YAML with `kind: Canary` YAML for each workload
- Change the analysis provider syntax (`AnalysisTemplate` → Flagger `MetricTemplate`)
- Controller Helm swap

Estimated migration effort: half a day per workload pair, dominated by retesting. This gives us permission to choose decisively now and reverse later without sunk-cost lock-in.

## Alternatives Considered

### A. Flagger

**Rejected.** Covered in §"Why Argo Rollouts" B1–B3. The honest summary: Flagger is an excellent tool in the ecosystem it's designed for (Flux + service mesh). We're in neither.

If we ever adopt Istio / Linkerd ambient mesh for other reasons (mesh-native observability, fleet-wide mTLS, multi-cluster federation — see ADR-0031's Phase 5 placeholder), **the Flagger vs Argo Rollouts evaluation gets re-opened**. Not the reverse: Argo Rollouts works equally well on top of Istio if one ever lands.

### B. Vanilla `Deployment` rolling update, no progressive delivery

**Rejected.** ARCH §10.4 commits to SLO-gated canary + automatic rollback. Standard `Deployment` rolling update has neither; it shifts pods by percentage of replicas, not by traffic weight, and its "rollback" is "kubectl rollout undo" — a human-driven revert, not a metric-driven abort. Accepting vanilla rolling would mean breaking ARCH's Phase 4c commitment.

Relevant nuance: for the **demo horizon**, vanilla rolling is survivable. The Phase 4c scope narrows this risk — C-5a lands step-based weight canary *without* SLO gates (pure time-window progression), C-5b adds SLO gates when Phase 4d's metric pipeline emits. Even step-based-without-gate canary is strictly stronger than vanilla rolling (you get weight progression + fast-abort CLI); "no canary at all" is the strictly weakest option and fails the ARCH promise.

### C. Custom canary via two `Deployments` + `Service` selector manipulation

**Rejected.** Hand-rolled weight shifting via `Service.spec.selector` label churn is how people did canary in 2017. It lacks:
- Analysis integration (would need bespoke scripts)
- Automatic abort on failure
- ArgoCD UI integration
- Audit trail

The operational cost of building + maintaining these misses all the argoproj-native value we've already opted into via ArgoCD. Zero upside; strong downside.

### D. Keptn / Jenkins X progressive delivery

**Rejected without deep evaluation.** Both require stack opinions (Jenkins X brings Tekton, Keptn brings its own control plane). The ArgoCD decision in ldz already rules them out by ecosystem fit.

## Honest phasing — C-5 splits into C-5a and C-5b

ARCH §10.4 promises SLO-based canary gates. **SLO gates need a metric source.** Our metric source is Phase 4d (OTLP → Prometheus or CloudWatch Metrics). Until Phase 4d lands, we cannot wire real SLO analysis; therefore:

- **C-5a (in Phase 4c scope)**: `Rollout` CRD deployed; canary weight progresses by **time-based step** (e.g., 10% → 30% → 60% → 100%, each step holds 5 minutes). No `AnalysisTemplate`, no rollback-on-error-budget. Manual abort via `kubectl argo rollouts abort` is always available. This is strictly better than vanilla rolling: traffic-weighted progression + fast abort + ArgoCD-visible state machine.
- **C-5b (at Phase 4c → 4d boundary)**: `AnalysisTemplate` wired to Phase 4d's metric provider. Error-budget burn rate >25% triggers automatic abort; SLO restoration triggers resumption. This closes the ARCH §10.4 commitment in full.

Choosing Argo Rollouts now enables both phases; choosing Flagger would require the same split and the same wait for metrics. The phasing is orthogonal to the controller choice — it's a Phase 4d blocker, not a controller trade-off.

## Consequences

### Positive

- Path of least friction: ArgoCD-native, ALB-native, no mesh required to ship C-5a.
- ADR ecosystem alignment — the argoproj bet (ArgoCD + Rollouts + Argo Events if ever needed) is coherent.
- SLO-gate wiring is CRD-level config in Phase 4d; no controller swap or re-architecture needed at the 4c → 4d boundary.
- Portfolio signal: picking the tool that matches the deploy platform is stronger than picking the fashionable one.

### Negative

- Binds further to the argoproj ecosystem. If argoproj ever stagnates or fragments, we've doubled down.
- Does not exercise service-mesh primitives in Phase 4c — anyone wanting to demo mesh traffic management on this project needs to do that explicitly under Phase 5 (ADR-0031 Phase 5 placeholder covers this escape hatch).
- `Rollout` CRD is a divergence from the vanilla `Deployment` every K8s learner starts with; contributors unfamiliar with argoproj will need to read the Argo Rollouts docs. This is a one-page onboarding cost, not a recurring one.

### Neutral

- Observability for canary progress lives in the Argo Rollouts UI (kubectl plugin + ArgoCD UI panel). No separate dashboard needed in Phase 4c.
- `AnalysisTemplate` syntax becomes a Phase 4d deliverable; no Phase 4c work forced on the metrics side.

## Trigger to re-open this decision

1. **Istio / Linkerd / App Mesh adopted** for any non-canary reason (e.g., mesh-native observability, fleet-wide mTLS via sidecar, multi-cluster federation). In that world, Flagger's mesh-native model becomes a genuine competitor and the trade-offs shift.
2. **Multi-cluster canary** requirement emerges (e.g., progressive rollout across regions with coordinated analysis). Flagger has stronger multi-cluster primitives today; Argo Rollouts catches up but lags.
3. **aws-load-balancer-controller abandoned** in favor of a non-ALB ingress that Argo Rollouts' plugin matrix doesn't cover. Low probability on EKS.

None of these are on Phase 4c's ROADMAP; re-opening remains a speculative future concern.

## Implementation checklist (tracked in ROADMAP Phase 4c)

- [ ] Install Argo Rollouts controller to `argo-rollouts` namespace on ldz EKS (ldz-side Helm / manifest — open cross-repo issue)
- [ ] Install ALB traffic-router plugin: `argoproj-labs/rollouts-plugin-trafficrouter-alb`
- [ ] Convert `apps/staging/aegis-core-gateway/deployment.yaml` → `rollout.yaml` (C-5a)
- [ ] Convert `apps/staging/aegis-core-engine/deployment.yaml` → `rollout.yaml` (C-5a)
- [ ] Define step-based canary strategy (10 → 30 → 60 → 100, 5 min each) — no `AnalysisTemplate` yet
- [ ] Wire `AnalysisTemplate` referencing Phase 4d metric source once it emits (C-5b)
- [ ] Playbook entry for `kubectl argo rollouts abort` manual override

## Cost summary

- **Controller memory** on EKS: ~100MB (single `argo-rollouts` controller pod)
- **Per-workload overhead**: zero (Rollout CRD replaces Deployment, same pod count)
- **CI cost**: zero (no new pipeline steps; deploy manifests are just different YAML)
- **Licensing**: $0 (Apache-2.0)
- **Ongoing operational complexity**: low — one controller, one CRD family, one CLI plugin
