# ADR-0036: Deploy topology — the `aegis-platform` tier and per-workload deploy repos

| Field    | Value |
| -------- | ----- |
| Status   | Proposed — aegis-core stance; cross-repo items pending agreement on ldz#214 |
| Date     | 2026-05-18 |
| Deciders | Project author |
| Context  | aegis-core's Kubernetes deploy manifests currently live inside the branch-protected `aegis-core` repo (`apps/staging/`), which makes the CI image-tag automation fight branch protection (ADR-0032, Incident 18). This ADR records aegis-core's stance on the deploy topology — a three-layer split (account fabric / platform tier / workload) with the platform tier extracted into a shared `aegis-platform` repo and each workload's manifests in its own deploy repo. It is the decision artifact behind cross-repo issues ldz#213 / #214 / #215. |
| Related  | ADR-0032 (CI image-tag automation — the degraded mode + Incident 18 branch-protection wall this topology removes); `aegis-aws-landing-zone` ADR-033 (landing-zone descope to account fabric; EKS / ArgoCD / observability reclassified as an extractable Platform tier); `aegis-aws-landing-zone` ADR-007 (infra/app repo split — rejects application manifests in the infra repo); cross-repo ldz#213 / #214 / #215; the `aegis-greeter` ↔ `aegis-stateless` pair (the proven two-repo GitOps precedent this topology generalizes) |

## Context

aegis-core's deploy manifests are welded into the application repo at
`apps/staging/`. Because `aegis-core` is a public repo with a `main-protection`
ruleset, every CI image-tag bump is a write to a protected branch — the wall
recorded in ADR-0032 and Incident 18. The automation can only run in degraded
mode (the bot opens a PR, a human merges it).

Concurrently the landing-zone is correcting its own scope: `aegis-aws-landing-zone`
ADR-033 descopes that repo to the pure *account fabric* (AWS Organizations, OUs,
SCPs, Identity Center, the centralized security baseline) and reclassifies the
EKS cluster, ArgoCD, and the observability stack as an extractable **Platform
tier**.

The `aegis-greeter` ↔ `aegis-stateless` pair already proves a clean two-repo
GitOps split works in this ecosystem: the application repo's CI writes an image
tag into a separate, unprotected infra repo, and that repo's ArgoCD reconciles.
This ADR generalizes that proven pattern into an explicit three-layer model and
records which repo owns which layer — so the layers stay *independently
extractable* rather than slowly re-coupling.

## Decision

**Adopt a three-layer repo topology — account fabric / platform tier / workload
(app + deploy) — with a single shared `aegis-platform` repo for the platform
tier and a per-workload deploy repo (`aegis-core-deploy`) for aegis-core's
manifests. The workload↔platform boundary is governed by an explicit contract
(D4) so the layers stay independently extractable.**

### D1. Three layers, one responsibility per repo

| Layer | Responsibility | Repo |
| --- | --- | --- |
| Account fabric | AWS Organizations / OUs / SCPs / Identity Center / guardrails / central audit | `aegis-aws-landing-zone` (v2) |
| Platform tier | VPC / EKS / ArgoCD / cluster add-ons / observability stack | `aegis-platform` |
| Workload — app | Application code only | `aegis-core`, `aegis-greeter` |
| Workload — deploy | K8s manifests; ArgoCD watches one per workload | `aegis-core-deploy`, `aegis-greeter-deploy` |

### D2. Platform tier — a single shared, neutrally-named `aegis-platform`

- **One** platform tier, repo `aegis-platform`. The name is deliberately neutral
  (not `aegis-core-platform`): it serves multiple workloads, and naming it after
  any one workload would re-couple identity — a form of deep coupling that
  forecloses cheap reuse later.
- Extracted from `aegis-stateless`'s proven `terraform/modules/regional-stack`
  (the "successfully landed" reference implementation, with per-cluster ArgoCD
  and read-only-deploy-key repo auth). **Not** rewritten from scratch, **not**
  extracted from the descoping `aegis-aws-landing-zone`, and **not** folded into
  `aegis-stateless` — folding it in would re-mix two workloads' platform in one
  repo, the exact scope creep ldz ADR-033 is itself correcting.
- ldz ADR-033 independently extracts a Platform tier out of the landing-zone.
  The two extractions **MUST converge on one `aegis-platform`**, not produce
  two. This is the central ask of ldz#214.
- aegis-core runs as a **hardened namespace tenant** on the shared cluster
  (Kyverno audio-isolation per ADR-0005 R6, default-deny NetworkPolicy). It does
  not require a dedicated VPC/cluster today — see "when to revisit".

### D3. aegis-core's deploy manifests → `aegis-core-deploy`

- aegis-core's `apps/staging/` manifests move **out** of the branch-protected
  `aegis-core` repo into a new `aegis-core-deploy` repo. This is ldz#214
  Option B (a dedicated GitOps deploy repo), endorsed by the landing-zone.
  Option A — manifests into `aegis-aws-landing-zone` — was rejected, as it
  contradicts ldz ADR-007.
- `aegis-core-deploy` is aegis-core-owned and carries the full-repo-name prefix
  (CLAUDE.md Rule 11). Its `main` keeps CI status checks and signed commits but
  has **no human-review gate on the tag-bump path** — that is what removes the
  ADR-0032 / Incident 18 branch-protection wall and lets the image-tag
  automation run fully, not degraded.
- The existing `apps/staging/` is *relocated*, not rewritten (task #8). The
  relocation also resolves the residual `apps/staging/aegis-policies/` →
  `aegis-core-policies` rename omitted from the ADR-0036-unrelated Rule 11 pass
  (PR #129).

### D4. Workload↔platform contract — the anti-coupling invariant

The layers stay cheaply separable only if the boundary is written down and held.

| Party | Provides |
| --- | --- |
| `aegis-platform` | The EKS cluster; the ArgoCD install; one ArgoCD `Application` CR per workload deploy repo; ArgoCD repo credentials (read-only deploy key, never a PAT); cluster add-ons (ingress, cert-manager, observability Alloy); the workload namespace + baseline guardrails (Kyverno). |
| A workload **deploy** repo | Valid K8s manifests under a defined path; an image-tag field the workload's CI writes; a `main` with no human-review gate on the tag-bump path. |
| A workload **app** repo | Signed + SBOM-attested OCI images in ECR; a CI step that writes the image tag into its deploy repo. |

**Boundary rule (the invariant):** `aegis-platform` contains **zero
workload-specific values**. A workload is configured only through its ArgoCD
`Application` CR and its deploy repo — never by editing `aegis-platform`. Hold
this and the tiers stay extractable; break it and they re-couple.

### D5. Extraction-readiness is a maintained property, not a scheduled event

- The layers remain cheaply separable because the D4 boundary is *held*, not
  because a template was built early. The thing that goes stale and forecloses
  reuse is the boundary, not a missed "template moment".
- `aegis-platform` is born extraction-ready: neutral name, contract written,
  zero workload-specific hardcoding.
- Templates are **trigger-conditioned future work, not built now**:
  - `aegis-service-template` (golden-path new-service repo) — trigger: stamping
    a 2nd service.
  - `aegis-platform-template` — trigger: the 1st workload that genuinely needs a
    dedicated VPC/cluster, distilled from the by-then-proven `aegis-platform`.
  Building either speculatively now is premature abstraction — a generator with
  no real instance to distil from drifts. Distil templates from a proven
  reference, on a trigger (the Rule of Three).

## Consequences

- Once `aegis-core-deploy` exists, the CI image-tag automation runs fully — this
  supersedes ADR-0032's degraded mode.
- One more repo for aegis-core to own (`aegis-core-deploy`), and the cross-repo
  image-tag write needs a fine-grained token (`contents:write`, scoped to that
  repo, expiring) — the standing cost the in-repo model avoided, accepted in
  exchange for removing the branch-protection fight.
- The platform extraction is cross-repo, cross-session work; the sequence below
  must be followed so the landing-zone and aegis-core do not produce two
  divergent platform tiers.
- Until the move lands, ADR-0032 degraded mode stands; nothing is blocked live
  (the landing-zone is torn down).

## Alternatives considered

- **A. Manifests stay in `aegis-core` (status quo).** The branch-protection
  wall — ADR-0032 / Incident 18. Rejected.
- **B. Manifests into `aegis-aws-landing-zone` (ldz#214 Option A).** Rejected —
  contradicts ldz ADR-007 (no application manifests in the infra repo).
- **C. Fold the platform tier into `aegis-stateless`.** Rejected — re-mixes two
  workloads' platform in one repo and couples the platform's identity to
  `aegis-greeter`; it would repeat the scope creep ldz ADR-033 is correcting.
- **D. Build `aegis-platform-template` / `aegis-service-template` now.**
  Rejected — premature abstraction; a template distils from a proven instance,
  on a trigger (D5).

## Out of scope / when to revisit

- **The greeter-side overlay split and `aegis-stateless`'s post-extraction
  fate** — a `aegis-greeter` / `aegis-stateless`-side decision, not aegis-core's.
  This ADR only commits aegis-core's half (`aegis-core-deploy`).
- **aegis-core getting a dedicated VPC/cluster** — revisit trigger: a concrete
  compliance / data-residency / blast-radius driver that namespace-tenancy plus
  Kyverno hardening cannot satisfy. None exists today.
- **The physical extraction of `aegis-platform` and the ArgoCD `Application`
  re-pointing** — cross-repo execution, sequenced after ldz#214 agreement.

### Execution sequence (recorded for the cross-repo issues)

1. Agree this topology and the single-`aegis-platform` convergence (ldz#214).
2. Extract `aegis-platform` from `aegis-stateless`'s `regional-stack`; ldz
   ADR-033's platform extraction converges onto it (not a second repo).
3. Create `aegis-core-deploy`; relocate aegis-core `apps/staging/` (task #8);
   retarget `release-staging-image.yml`'s tag-bump to write cross-repo.
4. The aegis-core ArgoCD `Application` re-points its `source` at
   `aegis-core-deploy`.
