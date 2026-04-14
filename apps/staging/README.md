# `apps/staging/` — Cloud-mode Kubernetes manifests

This directory is the **GitOps source of truth** for the Cloud-mode
EKS deployment of Aegis Core in the staging environment. ArgoCD
running in the
[`aegis-aws-landing-zone`](https://github.com/BinHsu/aegis-aws-landing-zone)
cluster polls this path recursively and reconciles whatever Kubernetes
manifests live below it onto the `aegis-staging` cluster.

The contract is documented in
[`aegis-aws-landing-zone#54`](https://github.com/BinHsu/aegis-aws-landing-zone/issues/54)
("Platform surface contract"), with the matching requirements list in
[`aegis-core#11`](https://github.com/BinHsu/aegis-core/issues/11).

## ⚠️ NOT relevant to the local-mode demo

If you cloned this repo to try the **single-machine experience** —

```bash
./tools/scripts/download_models.sh --all
./tools/bazelisk/bazelisk run //:app_local
```

— then **this directory has nothing to do with you**. Local mode runs
the C++ engine and the Go gateway as plain subprocesses on your
laptop; there is no Kubernetes, no ArgoCD, no EKS, no AWS. You can
ignore this entire `apps/` tree and the demo still works end-to-end
(see the README's [Quick Start](../../README.md#quick-start) section).

This tree exists so that **when** an Aegis-Core component is ready to
ship to the Cloud-mode EKS cluster — Phase 4a (OCI packaging) and
later — its Deployment / Service / Ingress / ConfigMap manifests
land here without needing a separate "infra" PR cycle. The application
team owns its own Kubernetes spec; the platform team
(`aegis-aws-landing-zone`) owns the cluster the spec runs on.

## Current state — empty (deliberately)

Phase 3 ships the frontend dev experience and the Local-mode bundle.
Nothing here yet. The directory carries this README so:

1. The path **exists** in version control. ArgoCD's root Application
   has `path: apps/staging`, so without a tracked file at this path
   GitHub would not preserve the directory.
2. Future contributors landing K8s manifests have one canonical place
   to look (and a contract issue to read first).

## When you're ready to add something here

Sketch:

```
apps/staging/
├── README.md           ← this file
├── gateway/            ← Phase 4a candidate: Go Gateway Deployment + Service + Ingress
│   └── …
├── engine/             ← Phase 4a candidate: C++ Engine Deployment + Service (internal-only)
│   └── …
└── frontend/           ← Phase 4a candidate: static bundle Deployment + ALB
    └── …
```

Conventions you can rely on (per landing-zone#54 contract, Phase 3c
state):

- ArgoCD scans this path recursively with `CreateNamespace=true`, so
  any namespace referenced in your manifests is auto-created.
- IRSA roles use the naming convention `aegis-staging-<app-name>` —
  request new roles via a `cross-repo` issue on `aegis-aws-landing-zone`.
- Karpenter is the only node provisioner; default NodePool is amd64
  Bottlerocket Spot, instance category t/m/c (gen > 2), 2-4 vCPU per
  node, capped at 4 vCPU total for the lab.
- AWS Load Balancer Controller handles `Ingress` and
  `Service type=LoadBalancer` translation; ACM cert ARNs come from
  landing-zone outputs.
- Kubernetes API version baseline: 1.32.

For anything you need that isn't covered above, check the contract
issue first (it gets edited as the contract evolves), then open a new
`cross-repo` (or `cross-repo/blocking`) issue on the sibling repo.
