# `deploy/` — Kubernetes manifests for ArgoCD

This directory holds the Kubernetes manifests that `aegis-aws-landing-zone`'s ArgoCD controller polls and syncs into the staging EKS cluster. It is the application-side half of the GitOps boundary documented in **ARCHITECTURE.md §7 Cross-Repository Cloud Infrastructure**.

## Current scope

- **`staging/`** — the only environment manifested today. A single-environment layout (plain YAMLs, no Kustomize/Helm overlays) is intentional for Phase 4c; multi-environment layering becomes warranted when prod lands.

## Convention

| Decision | Value | Reference |
|---|---|---|
| Environment layout | Single directory per env under `deploy/` | Phase 4c C-1 |
| Manifest tooling | Plain YAML (no Kustomize / Helm) | Minimise moving parts while `n=1 environment` |
| Image registry | `${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com/aegis-core` | ldz #11 platform contract |
| Image tag format | `staging-<git_sha>` (gateway) · `engine-staging-<git_sha>` (engine) | ADR-0025, Phase 4a Slice 3 |
| Security posture | distroless nonroot + readOnly rootfs + drop ALL caps | ADR-0025, ADR-0005 R6 |
| Namespace | `aegis` (all workloads) | `staging/namespace.yaml` |
| Service topology | Gateway: ClusterIP + ALB Ingress · Engine: **Headless** (gRPC round-robin) | ADR-0017 |
| mTLS | cert-manager-issued certs in CLOUD mode; absent in C-1 (arrives in C-2/C-5) | ADR-0031 |
| Progressive delivery | Plain `Deployment` in C-1; `Rollout` CRD replacement in C-5a | ADR-0030 |

## How ArgoCD consumes this

1. ArgoCD's `Application` resource (in `aegis-aws-landing-zone`, not here) points at `https://github.com/BinHsu/aegis-core` path `deploy/staging/`.
2. On every commit to `main`, ArgoCD diff-syncs resources against the cluster state.
3. `argocd.argoproj.io/sync-wave` annotations (if used) order the applications; C-1 uses default sync order (Namespace first, then workloads).

## Known gap — image tag updates (C-1.5, deferred)

Today's manifests reference a **specific bootstrap image SHA** (see each `deployment.yaml`). A single-commit update loop requires one of:

- **Argo CD Image Updater** — auto-scans ECR, commits the new tag back to this repo on each push.
- **CI-commits-tag** — `release-staging-image.yml` extended with a final step that `git commit`s the new tag into the manifest after successful push (push-based GitOps).

The decision between these two is **deferred to C-1.5**. Until then, the image tag in the manifest is a static reference to the last known-good SHA; ArgoCD will sync whichever SHA appears in `main`. This is not a deploy-time defect — it just means each release cycle currently requires one manual manifest-SHA edit. C-1.5 closes this loop.

## Not in C-1 scope (tracked for later slices)

- mTLS cert-manager Certificate CRs (C-2 — ADR-0031 wiring)
- Argo Rollouts `Rollout` CRD (C-5a — replaces Deployments)
- Kyverno/Gatekeeper ClusterPolicies (C-6 — audio-ns + cert-mount enforcement)
- PodDisruptionBudget (worth adding once replica counts reflect real load)
- NetworkPolicy (complements mTLS; best landed alongside C-2)
- HorizontalPodAutoscaler (needs Phase 4d metrics source first)
- Model PV for engine (ADR-0026 — engine pre-flight slice / ldz PV provisioning cross-repo)

## Honest gap at C-1

Engine pod mounts a placeholder `emptyDir` at `/models` — **engine will crash on startup** because models/manifest.json → actual .gguf files are missing from emptyDir. C-1's scope is "manifests that ArgoCD can sync"; "manifests that produce a running engine" is C-2 / ADR-0026 territory (persistent model storage + pre-flight validator). The gateway deployment is independently runnable and `/healthz`-responsive without the engine, so the foundation is exercisable for C-4 E2E against the gateway path even before the engine lands.
