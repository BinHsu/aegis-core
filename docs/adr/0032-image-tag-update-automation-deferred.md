# ADR-0032: Image Tag Update Automation — Deferred

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted (deferred — triggers documented)                                   |
| Date     | 2026-04-20                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4c C-1.5 was provisionally carved out of ROADMAP Phase 4c to answer "how does a newly-built staging image get its SHA into `apps/staging/*/deployment.yaml`?" This ADR closes the slice without shipping automation, with triggers for reopening. |
| Related  | ADR-0030 (Argo Rollouts — chose argoproj family), ADR-0031 (mTLS — same "reject the fashionable tool at our scale" reasoning), `apps/staging/README.md` §"Known gap — image tag updates" (preserved as documentation of the manual step until this decision reverses) |

## Context

After C-1 landed `apps/staging/` with hardcoded bootstrap image SHAs, the follow-up question was: **how does the next ECR push's tag get into these manifests?** Three mechanisms were on the table:

- **Argo CD Image Updater** — cluster-side controller polls ECR, commits a tag change back to this repo
- **CI-commits-tag** — `release-staging-image.yml` extended with a final step that rewrites the manifest and pushes
- **Manual edit in the next PR** — human edits the SHA as part of the next unrelated commit

A pre-requirement audit against the repo's actual branch-protection state revealed that **both automated paths (Image Updater and CI-commits-tag) hit the same wall**: the `main` ruleset (queried via `gh api repos/BinHsu/aegis-core/rulesets/14973346`) enforces:

- `required_signatures: true`
- `required_approving_review_count: 1` + `require_code_owner_review: true`
- `required_linear_history: true`
- Only `squash` merges allowed
- Bypass restricted to `RepositoryRole` admin (actor_id 5)

No bot (GitHub Actions' `github-actions[bot]` or an Image Updater service account) sits in the bypass list. Any automated push to `main` fails the rules; any automated PR still requires human CODEOWNERS review to merge.

This turns "automation" into "CI opens a PR that a human clicks approve on" — which reduces the per-release friction from ~10 seconds (manual SHA edit) to ~2 seconds (click merge), at the cost of adding either a new controller (Image Updater) or non-trivial workflow logic (commit-back step).

## Decision

**Defer the automation. No controller, no workflow extension.** Per release, the image SHA is updated as part of whatever PR naturally touches `apps/staging/` — typically the next Phase 4c / 4d slice PR. For code-only releases where no slice PR is pending, the SHA is updated in a one-line manual commit.

### Why this is the right call at current scale

**The friction is smaller than the automation cost.**

| Dimension | Current manual path | Best automated path (CI-opens-PR) |
|---|---|---|
| Per-release human action | Edit one line in one manifest | Click "approve + merge" on bot PR |
| Time per release | ~10 seconds | ~2 seconds |
| Release frequency (current) | ~3/week during active Phase development | — |
| Weekly time save | — | ~24 seconds |
| Annual time save | — | ~20 minutes |
| Setup cost (one-time) | — | ~4 hours (workflow + YAML edit logic + PR template + testing) |
| Ongoing maintenance | Zero | Small but non-zero (workflow drift, new dependency if `yq` used) |
| Adds to ldz cross-repo backlog | No | Yes (Image Updater variant) |

At 20 minutes of annual time save against ~4 hours of setup cost plus ongoing maintenance, **the automation pays back in over a decade**. That ratio makes sense only if the problem is load-bearing on the critical path; today it is not.

**The problem self-dissolves during active Phase development.** Every Phase 4c slice (C-2 mTLS certs, C-3 model storage populator, C-5a Rollout conversion, C-6 Kyverno policies) naturally touches `apps/staging/`. Those PRs absorb the SHA bump as part of the change. The only case where "code-only release without manifest touch" happens is CVE dependency bumps (e.g., Go SDK CVE fix) or pure engine algorithm changes — and those have been rare enough during Phase 4a and 4b that they don't justify preemptive automation.

**Branch protection is a deliberate design choice, not friction to route around.** The ruleset enforces that every change to `main` goes through PR + review + signed commits. Weakening that to enable a marginal automation (e.g., adding a bot to `bypass_actors`) would trade a durable security property for a small convenience. The honest move is to accept the protection and let it shape the release cadence.

## Alternatives Considered

### A. Argo CD Image Updater (cluster-side pull)

**Rejected.** Setup cost is the highest of all candidates — Image Updater is a full controller that ldz would need to install (adding to the cross-repo queue already carrying #101 / #102 / #103) and configure with ECR scan credentials + a write-back mechanism to this repo. Two write-back modes exist:

- `git` — same branch-protection wall; Image Updater uses a bot commit that would need bypass grant or would require human approval, same as CI-commits-tag
- `argocd` — mutates the live ArgoCD Application in-cluster, bypassing git entirely. This **breaks the GitOps invariant**: the cluster state no longer reflects `main`'s YAML. Unacceptable for a repo where `apps/staging/` is the declared source of truth.

Neither path is attractive for our scale.

### B. CI-commits-tag (push-side, workflow extension)

**Rejected for Phase 4c; may re-open under trigger.** A final step in `release-staging-image.yml` that rewrites the manifest's image tag and commits back. Two concrete shapes evaluated:

**B1. Direct push to `main`** — impossible without weakening protection. The GITHUB_TOKEN's default permissions don't bypass `required_signatures` or `require_code_owner_review`.

**B2. Open a PR, auto-merge on approval** — implementable, but the "approval" step defeats the automation goal at solo-owner scale (the approver is the same human who would have done the manual edit). Saves ~8 seconds per release, costs the setup + maintenance tax described above.

### C. Add `github-actions[bot]` to `bypass_actors` on the ruleset

**Rejected.** Technically straightforward (one-time ruleset edit), but the trade is poor:

- **Property lost**: "every change to `main` goes through human-reviewed, signed commits"
- **Property gained**: auto-updated image tags
- **Attack surface widened**: any bug in any CI workflow that produces a commit will land on `main` without review. Today, a buggy Dependabot-triggered commit gets caught at review time; under bypass, it lands immediately.

At the aegis-core trust-surface rating (`docs/threat-model.md`), `main`-write authority is a high-blast-radius primitive. Not willing to trade it for tag automation.

### D. Manual edit (current behaviour)

**Selected.** Current state, documented in `apps/staging/README.md` §"Known gap — image tag updates". Per-release cost is real but small; per-Phase-4c cost is zero because the slice PRs carry the edits.

## Triggers to revisit

Listed in order of likelihood. When any of these fires, open ADR-0033 "Image tag update automation — design" and pick between B2 (CI-opens-PR) or a variant.

1. **Release cadence exceeds manifest-change cadence by a consistent margin.** Example: Phase 5 enters maintenance mode; dependency bumps happen weekly but `apps/staging/` stays stable. At that point, the "natural edit in the next slice PR" argument dissolves.

2. **A second regular contributor joins.** Manual coordination on "who bumps the tag for this release?" becomes friction between humans; automation eliminates the coordination overhead. Solo-owner scale masks this cost.

3. **Multi-region deployment lands** (ROADMAP Phase 5 regional-routing line; ldz slot pattern primary + slave_1). Two clusters = two tag edits per release unless the manifest uses an `ApplicationSet` template parameter (per landing-zone #46 acceptance) — which itself benefits from automated population.

4. **A release-time defect traced to "forgot to bump the tag"** — i.e., the manual path's failure mode actually hurts, not just "is slower." If we see this once, document; if twice, automate.

5. **`bypass_actors` policy changes** for an unrelated reason, adding `github-actions[bot]` or a dedicated bot App. In that world the branch-protection wall that rejects Option C here falls; re-open this ADR under the new constraint.

## Consequences

### Positive

- **Zero setup cost at Phase 4c boundary.** No workflow edit, no ldz cross-repo ask, no new CRDs. Phase 4c's remaining implementation slices ship faster.
- **Branch protection integrity preserved.** Every commit on `main` continues to be signed + reviewed. The audit trail remains clean.
- **One fewer moving part to debug.** When a deploy doesn't reflect expected image, the answer is "the manifest in `main` has a specific SHA; check if it's the right one" — no "is Image Updater running?", "is the commit-back step silently failing?", "did the PR auto-merge get blocked by a check?" surface.
- **Portfolio signal, same family as ADR-0031.** Rejecting a fashionable tool with written trade-off analysis is the pattern Phase 4c is accumulating.

### Negative

- **Manual release cadence.** Every release still requires one human edit of the image SHA somewhere (usually absorbed by a slice PR; rarely a dedicated bump commit). For solo-dev this is ~10 seconds; for a hypothetical future team this is real coordination friction.
- **Dedicated bump PRs are noisy when they happen.** A PR titled *"chore(deploy): bump staging image tag to db14d67..."* is pure ops hygiene, carries no semantic change, but still requires the full CI matrix + review. Cost is small (CI is ~3 min warm) but non-zero.
- **No path to continuous deployment.** With CD in the purest sense ("every merge triggers a deploy"), the tag-update loop would be automatic. We're explicitly choosing "continuous integration + human-gated deploys" — the ArgoCD pulls from `main`, but the human decides when `main` gets a bumped tag.

### Neutral

- Does not change the shape of `apps/staging/` manifests; they continue to hold a literal SHA.
- Does not preclude future adoption of any of the rejected alternatives; triggers above name the revisit points.

## Implementation

**None required.** This ADR is the deliverable; no code change ships with it.

Companion edits:

- `apps/staging/README.md` §"Known gap — image tag updates" — rewrite from "deferred to C-1.5 (pending decision)" to "deferred per ADR-0032 (decision made)"
- ROADMAP Phase 4c — change C-1.5 line from `[ ] pending decision` to `[x] deferred per ADR-0032` with a one-line pointer
- ROADMAP Last Updated — note ADR-0032

## Cost summary

- **Phase 4c implementation cost**: 0 (no code)
- **Per-release ongoing cost**: ~10 seconds human edit (usually absorbed into unrelated slice PR)
- **Annual hand-time budget consumed**: ~20 minutes, subject to release-cadence assumption
- **Revisit cost if a trigger fires**: ~4 hours to implement B2 under new constraints

The total lifecycle cost of this decision is **lower than the implementation cost of any of the automated alternatives**, under any release-cadence scenario that doesn't fire a trigger.
