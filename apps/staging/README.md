# `apps/staging/` — Cloud-mode Kubernetes manifests

This directory is the **GitOps source of truth** for the Cloud-mode
EKS deployment of Aegis Core in the staging environment. ArgoCD
running in the [`aegis-aws-landing-zone`](https://github.com/BinHsu/aegis-aws-landing-zone)
cluster polls this path recursively (`path: apps/staging` +
`recurse: true`) and reconciles whatever Kubernetes manifests live
below it onto the `aegis-staging` cluster.

The contract is documented in
[`aegis-aws-landing-zone#54`](https://github.com/BinHsu/aegis-aws-landing-zone/issues/54)
("Platform surface contract"), with the matching requirements list in
[`aegis-core#11`](https://github.com/BinHsu/aegis-core/issues/11).

## ⚠️ NOT relevant to the local-mode demo

If you cloned this repo to try the **single-machine experience** —

```bash
./tools/scripts/download_models.sh
./tools/bazelisk/bazelisk run //:app_local
```

— then **this directory has nothing to do with you**. Local mode runs
the C++ engine and the Go gateway as plain subprocesses on your
laptop; there is no Kubernetes, no ArgoCD, no EKS, no AWS. You can
ignore this entire `apps/` tree and the demo still works end-to-end.
Per ADR-0031 §"LOCAL mode posture", mTLS is also bypassed in LOCAL
mode — plaintext gRPC over localhost, by design.

## Current state — Phase 4c C-1 + C-5a + C-Obs-1 landed; (b) seed Job in place

| Component | Resource kinds |
|---|---|
| Gateway | `Rollout` (ADR-0030) · `Service` (ClusterIP) · `Ingress` (ALB) · `ServiceAccount` · `NetworkPolicy` · `ServiceMonitor` |
| Engine | `Rollout` (ADR-0030) · `Service` (**headless**, ADR-0017) · `ServiceAccount` (IRSA-annotated) · `NetworkPolicy` · `ServiceMonitor` · `Job` (RAG corpus seed, ArgoCD `PostSync` hook) |
| Policies | Kyverno `ClusterPolicy` (audio-ns no-PVC + no-hostPath, ADR-0005 R6, Audit mode) |

**Not here yet** (tracked in ROADMAP Phase 4c):

- cert-manager `Certificate` CRs for mTLS — C-2, ADR-0031
- `PodDisruptionBudget` — worth adding once replica counts reflect real load
- Model `PersistentVolumeClaim` or S3-CSI mount — C-3, ADR-0026 (gated on ldz #85 IAM extension)
- Argo Rollouts `AnalysisTemplate` SLO gates — C-5b, depends on Phase 4d metric pipeline burning realistic baselines

## Convention

| Decision | Value | Reference |
|---|---|---|
| Layout | Single directory per env under `apps/` (plain YAML, no Kustomize/Helm) | Phase 4c C-1 |
| Image registry | `251774439261.dkr.ecr.eu-central-1.amazonaws.com/aegis-core` | ldz #54 platform contract |
| Image tag format | `staging-<git_sha>` (gateway) · `engine-staging-<git_sha>` (engine) | ADR-0025, Phase 4a Slice 3 |
| Security posture | distroless nonroot + readOnly rootfs + drop ALL caps | ADR-0025 |
| Namespace | `aegis` (Terraform-managed by ldz — do NOT create here) | ldz #54 |
| Service topology | Gateway: ClusterIP + ALB Ingress · Engine: **Headless** (gRPC round-robin DNS) | ADR-0017 |
| mTLS | cert-manager-issued certs in CLOUD (absent in C-1; arrives in C-2) | ADR-0031 |
| Progressive delivery | `Rollout` (Argo Rollouts CRD) — C-5a landed; `AnalysisTemplate` SLO gates pending C-5b | ADR-0030 |

## Platform guarantees from ldz #54

- **AWS Load Balancer Controller** pre-installed — `Ingress` translates to ALB automatically
- **Default-deny NetworkPolicy** already on `aegis` ns — we add explicit allow rules below
- **Kyverno Audit mode** — our Rollouts satisfy all 4 baseline ldz ClusterPolicies (non-privileged, no host-ns, resource limits present, `app.kubernetes.io/name` labelled), plus our own audio-ns isolation policy in `aegis-policies/kyverno-audio-isolation.yaml` (ADR-0005 R6)
- **Karpenter vCPU cap: 4 total** — our requests sum to 1.2 vCPU; fits comfortably
- **IRSA role pre-provisioned**: `aegis-staging-aegis-engine` with trust scope `system:serviceaccount:aegis:aegis-engine`. Engine SA carries the `eks.amazonaws.com/role-arn` annotation; permission policy on the role is currently empty (skeleton — attaches when engine gains AWS API surface).

## Image tag updates — manual, by design (ADR-0032)

Manifests reference a **specific image SHA** as a literal. Each release cycle, the SHA is updated either inside whatever Phase 4c / 4d slice PR is already touching `apps/staging/` (the common case during active development) or via a small dedicated bump commit when no slice PR is pending.

Automation for this (Argo CD Image Updater or CI-commits-tag) was deliberately rejected in [ADR-0032](../../docs/adr/0032-image-tag-update-automation-deferred.md): at current release cadence (~3/week) and branch-protection shape (signed + reviewed commits), automation pays back in over a decade. Triggers to revisit are documented in the ADR.

**Until a trigger fires, this is not a gap — it is the chosen release workflow.**

## Known gap — engine will crashloop on first sync

Engine pod mounts a placeholder `emptyDir` at `/models`. The actual model files (`models/ggml-tiny.en.bin` etc.) aren't present in this volume, so the engine binary fails startup with `couldn't open file`. This is expected for C-1 — manifest-level structure is the deliverable; the model storage layer is **C-3** (ADR-0026 S3 populator) and requires ldz-side IAM extension (ldz #85).

The gateway path remains independently runnable and `/healthz`-responsive even without a working engine, so C-4 E2E against the gateway API can proceed on its own clock.

## Known gap — ingress needs ldz ACM cert + Route53

Gateway `Ingress` annotates a placeholder ACM certificate ARN and references hostname `aegis-api.staging.binhsu.org`. Both require ldz provisioning:

- DNS-validated ACM certificate in `eu-central-1`
- Route53 A/ALIAS record to the ALB hostname

Tracked via a cross-repo issue on the ldz side. On first ArgoCD sync, the Ingress will reconcile to a healthy ALB **only** after ldz closes those two tickets; until then, ArgoCD will show the Ingress as pending, which is expected on bring-up and not a manifest failure.

## Cross-repo operations model

Per ARCH §7 and ldz #54 stability clause:

- Contract changes are announced on ldz #54 (edit, never close)
- Discrete asks open fresh `cross-repo` issues on the ldz side
- Label `cross-repo/blocking` when the ask gates our progress; `cross-repo/fyi` for informational
- Reciprocal issue on this repo: [`aegis-core#11`](https://github.com/BinHsu/aegis-core/issues/11) carries our requirements list
