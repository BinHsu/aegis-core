# ADR-0032: Image Tag Update Automation

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted (re-decided 2026-05-17 — automation implemented; supersedes the 2026-04-20 deferral below) |
| Date     | 2026-04-20 (original deferral) · 2026-05-17 (re-decided — automation accepted) |
| Deciders | Project author                                                              |
| Context  | Phase 4c C-1.5: "how does a newly-built staging image get its SHA into `apps/staging/**`?" The 2026-04-20 version deferred automation. The 2026-05-17 revision reverses that and ships CI automation, on the strength of a same-repo observation that removes the original cost argument. |
| Related  | ADR-0030 (Argo Rollouts), ADR-0027 (GH Variables over hardcode), ADR-0028 (Cosign keyless — same workflow), `.github/workflows/release-staging-image.yml` (the `bump-image-tag` job), `apps/staging/README.md` §"Image tag updates", `docs/github-setup.md` §1 (the `main` ruleset this interacts with) |

> **The ADR filename keeps the `-deferred` suffix for URL stability** (it is
> referenced by hash from `apps/staging/README.md`, `ROADMAP.md`, and prior
> commits). Per the `docs/adr/` convention — see ADR-0026, which was "revised"
> in place rather than renamed — a re-decided ADR updates its **Status** field
> and adds a revision section; it does not get a new filename. The suffix is
> now historical; the Status field is authoritative.

## Revision (2026-05-17) — automation accepted

The 2026-04-20 deferral (preserved verbatim under "Superseded reasoning" below)
rested on one load-bearing cost claim: that *any* automated tag bump hits the
`main` branch-protection wall and therefore needs either a new cluster-side
controller (Argo CD Image Updater) or a fine-grained PAT / bot-credential to
push back — a second standing credential to provision, rotate, and secure.

**That claim was wrong about this repo's topology**, and the error is worth
naming because it is exactly the kind of premise that decays silently:

- The 2026-04-20 analysis implicitly carried over a **cross-repo** mental model
  (manifests in one repo, CI in another — the shape ADR-0032's "Adds to ldz
  cross-repo backlog" row assumed). In that world, a write-back genuinely needs
  a credential that crosses the repo boundary.
- But **aegis-core's Kubernetes manifests live IN THIS SAME REPO** —
  `apps/staging/aegis-core-gateway/rollout.yaml`, `apps/staging/aegis-core-engine/rollout.yaml`,
  `apps/staging/aegis-core-engine/seed-job.yaml`. ArgoCD (in the landing-zone
  cluster) *polls* this path; it does not host the manifests.
- A same-repo tag bump therefore needs **only the workflow's built-in
  `GITHUB_TOKEN`** (`permissions: contents: write` + `pull-requests: write`).
  No fine-grained PAT. No Image Updater controller. No second standing
  credential, no rotation surface, no cross-repo ask.

That single correction collapses the original cost column. The setup is a
~60-line job in an *already-existing* workflow (`release-staging-image.yml`),
not a 4-hour new-controller install. With the cost an order of magnitude
lower than estimated, the ~20-minutes-per-year time save is no longer the only
benefit on the table — the real win is **eliminating a release-time failure
mode** ("forgot to bump the tag", trigger #4 of the original ADR) before it
ever fires, rather than waiting for it to hurt twice.

### Decision (re-decided)

**Implement CI automation.** `release-staging-image.yml` gains a `bump-image-tag`
job that runs after the gateway + engine images are built, pushed, signed,
attested, and CVE-scanned. The job:

1. Rewrites the `image:` field of the three `apps/staging/**` workloads to the
   just-pushed tags (`staging-<sha>` for the gateway, `engine-staging-<sha>` for
   the engine Rollout and the seed Job) using `yq` (preinstalled on
   `ubuntu-latest`).
2. Opens a **pull request** with that change and enables **auto-merge**.
3. Once the PR's required status checks pass, it squash-merges to `main` with
   no human click, and ArgoCD reconciles the new tags onto the cluster.

The decision deliberately keeps the change flowing **through a PR** rather than
a direct push — see the branch-protection section below.

## Branch-protection handling

The `main` ruleset (`docs/github-setup.md` §1) enforces, among other rules:

- `required_signatures: true`
- a `pull_request` rule — `required_approving_review_count: 1` +
  `require_code_owner_review: true`
- `required_linear_history: true`, squash-only merges

Two parts of that wall have to be answered honestly (CLAUDE.md Rule 8 — no
silent `--admin`, no `--no-verify`):

### 1. `required_signatures` — solved in-workflow, no ops change

A plain `git push` from a GitHub-hosted runner produces an **unsigned** commit;
it would be rejected on merge. Rather than provisioning a GPG key as a workflow
secret (a new standing credential — the exact thing the same-repo observation
let us avoid), the `bump-image-tag` job creates the commit through the GitHub
GraphQL **`createCommitOnBranch`** mutation. Commits made via that API are
**signed server-side by GitHub's own key** and land `Verified`. The
`required_signatures` rule is satisfied with zero key material in the repo.

### 2. Code-owner review on the bot PR — one-time ruleset change required

A bot-authored PR cannot self-approve, and `require_code_owner_review: true`
means the auto-merge PR would otherwise stall waiting for a human click —
which defeats the "no manual step" goal.

**Chosen handling:** add `github-actions[bot]` to the `main` ruleset's
`bypass_actors` list with **`bypass_mode: pull_request`** (NOT `always`). This
is a deliberate, documented configuration decision, not a silent bypass:

- `bypass_mode: pull_request` lets the bot's change land **only through a pull
  request** — the PR is still created, the full required-status-checks gate
  (Pre-commit, Gitleaks, Proto lint/codegen, Markdown link check, Bazel unit
  tests) still runs and still blocks a red merge. What the bot bypasses is the
  *human code-owner review*, not the *PR* and not *CI*.
- It does **not** grant direct-push bypass. The bot cannot push to `main`
  outside a PR. This is strictly narrower than the `bypass_mode: always` grant
  the repo admin (`actor_id 5`) already holds.
- The blast radius is bounded: the only workflow that produces a PR as
  `github-actions[bot]` and enables auto-merge is `release-staging-image.yml`,
  whose OIDC trust scope is already pinned by the landing-zone IAM trust policy
  to `ref:refs/heads/main` + this exact `job_workflow_ref`. A buggy *other*
  workflow producing a commit still surfaces as an un-auto-merged PR.

**REQUIRED ONE-TIME OPS STEP — cannot be done from code in this PR.** Ruleset
membership is repo configuration, not a file in the tree. Before the
`bump-image-tag` job's PR can auto-merge unattended, the repo admin must run:

```bash
REPO=BinHsu/aegis-core
RULESET_ID=$(gh api repos/$REPO/rulesets --jq '.[] | select(.name=="main") | .id')

# Fetch the current ruleset, append github-actions[bot] as a
# pull_request-scoped bypass actor, PUT it back (gh PUT replaces the
# whole ruleset — see docs/github-setup.md §1).
#
# github-actions[bot] is an *Integration* actor; its actor_id is the
# GitHub Actions app id (15368 — the integration_id already used by the
# required_status_checks contexts in docs/github-setup.md §1).
#
#   {"actor_id": 15368, "actor_type": "Integration", "bypass_mode": "pull_request"}
#
# Add that object to the existing "bypass_actors" array (keep actor_id 5,
# the Repository admin role) and PUT the full ruleset payload back.
```

Until that change is applied, the automation **degrades gracefully**: the
`bump-image-tag` job still opens the PR and enables auto-merge; the PR simply
waits for a manual approval click before merging. Even degraded, this is
strictly better than the old fully-manual edit — the SHA is already correct in
the PR diff, CI has already validated it, and the human action shrinks from
"find the SHA, edit three files, sign the commit" to "click approve".

### Alternative considered for the review wall — rejected

A repository-ruleset *bypass actor scoped to the workflow* via the more
permissive `bypass_mode: always` was considered and rejected: `always` would
also permit direct (non-PR) pushes to `main`, widening the grant beyond what
the tag bump needs. `pull_request` mode is the minimum grant that achieves the
goal, so it is the one chosen.

## Consequences (re-decided)

### Positive

- **Zero manual step per release.** After the one-time ruleset change, a merge
  to `main` builds the images and the deployed tag updates itself; ArgoCD
  reconciles. The "forgot to bump the tag" failure mode (original trigger #4)
  is designed out before it fires.
- **No new standing credential.** Built-in `GITHUB_TOKEN` only — no PAT to
  provision/rotate, no Image Updater controller, no cross-repo ldz ask. This is
  the whole reason the re-decision is cheap.
- **Branch-protection integrity preserved.** Every change to `main` still
  arrives via a PR that passes the full CI gate, and every commit is still
  signed (`Verified`, via `createCommitOnBranch`). The only relaxation is a
  *narrowly* scoped, *documented* `pull_request`-mode bypass of human review
  for one bot — recorded here, not slipped in.
- **GitOps invariant intact.** The tag lives as a literal in `apps/staging/**`
  in `main`; cluster state still reflects the repo's YAML exactly. (The
  rejected Image Updater `argocd` write-back mode would have broken this — see
  the superseded analysis.)

### Negative

- **One-time ops step is a real prerequisite.** Until the admin adds the
  `pull_request`-mode bypass actor, the PR waits for a manual approval. This is
  called out in the PR body and above so it cannot be silently forgotten.
- **One bot PR per release.** Each release produces a `ci(deploy): bump staging
  image tags …` PR. It is pure ops hygiene and carries no semantic change, but
  it still consumes a CI run (~3 min warm). Acceptable — it is the audit record
  of which commit produced which deployed tag.
- **`createCommitOnBranch` is a GitHub-API dependency.** If GitHub changes that
  mutation's contract the bump job breaks. Low risk (stable public API), and the
  failure is loud (job goes red), not silent.

### Neutral

- `apps/staging/**` manifests still hold a literal image reference; the job
  rewrites the literal rather than introducing a Kustomize/Helm parameter. No
  manifest-shape change.
- The `release-staging-image.yml` `paths-ignore: apps/**` filter remains
  load-bearing — it keeps the bump merge from retriggering the workflow (an
  otherwise-infinite build→push→bump loop). Documented inline in the workflow.

## Testing posture

This slice is YAML + Markdown only — no Go/C++/proto code, so there is no
Bazel-testable unit. `actionlint` (pre-commit) validates the workflow syntax.
The behavioural contract — "a merge to `main` opens a correct tag-bump PR that
auto-merges" — is **a cross-process CI behaviour that no unit test can cover**;
per CLAUDE.md Rule 2's escape-hatch clause, the layer that *would* catch a
regression here is the **next real release run** of `release-staging-image.yml`
on `main` (the first post-merge release is the live integration test). This is
named explicitly rather than omitted.

---

## Superseded reasoning (2026-04-20 — original deferral, preserved as history)

> The text below is the original ADR-0032 decision. It is **no longer in
> effect** — the Revision above reverses it — but is kept verbatim so the
> reasoning trail (and the premise error the Revision corrects) stays auditable.

### Context (original)

After C-1 landed `apps/staging/` with hardcoded bootstrap image SHAs, the
follow-up question was: **how does the next ECR push's tag get into these
manifests?** Three mechanisms were on the table:

- **Argo CD Image Updater** — cluster-side controller polls ECR, commits a tag change back to this repo
- **CI-commits-tag** — `release-staging-image.yml` extended with a final step that rewrites the manifest and pushes
- **Manual edit in the next PR** — human edits the SHA as part of the next unrelated commit

A pre-requirement audit against the repo's actual branch-protection state
revealed that **both automated paths (Image Updater and CI-commits-tag) hit the
same wall**: the `main` ruleset enforces `required_signatures`,
`required_approving_review_count: 1` + `require_code_owner_review: true`,
`required_linear_history`, squash-only merges, and bypass restricted to
`RepositoryRole` admin (actor_id 5). No bot sat in the bypass list.

This turned "automation" into "CI opens a PR that a human clicks approve on" —
reducing per-release friction from ~10 seconds to ~2 seconds, at the cost of
adding either a new controller or non-trivial workflow logic.

### Decision (original — superseded)

**Defer the automation. No controller, no workflow extension.** Per release,
the image SHA was to be updated as part of whatever PR naturally touches
`apps/staging/`, or in a one-line manual commit for code-only releases.

The cost table argued ~20 minutes of annual time save against ~4 hours of
setup plus ongoing maintenance — a payback "in over a decade" — and concluded
the automation was not worth it at solo-owner scale.

> **Why this was reversed:** the ~4-hour setup estimate assumed a new
> controller or a cross-repo write-back credential. Once it was recognised that
> `apps/staging/**` lives in the *same* repo as the workflow, the setup
> collapsed to a ~60-line job in an existing workflow using the built-in
> `GITHUB_TOKEN` — no controller, no PAT. The cost side of the table was off by
> an order of magnitude, which inverts the conclusion. See the Revision above.

### Alternatives considered (original)

- **A. Argo CD Image Updater** — rejected; highest setup cost, and the `argocd`
  write-back mode breaks the GitOps invariant.
- **B. CI-commits-tag** — rejected *for Phase 4c*; B1 (direct push) impossible
  under `required_signatures`, B2 (open a PR) implementable but judged not
  worth the setup tax. **The Revision adopts a B2 variant** — the setup tax was
  overestimated, and the signing wall is cleared by `createCommitOnBranch`
  rather than by weakening protection.
- **C. Add `github-actions[bot]` to `bypass_actors`** — originally rejected as
  trading away "every change to `main` is human-reviewed". The Revision adopts
  a **narrower** form of this: `bypass_mode: pull_request` (not `always`), which
  bypasses only the *human review* step while keeping the PR + CI gate. The
  original rejection assumed the coarse `always` grant; the fine-grained
  `pull_request` mode is an acceptable, documented trade.
- **D. Manual edit** — originally selected; now superseded.

### Triggers to revisit (original)

The original ADR listed five triggers for reopening (release cadence exceeding
manifest-change cadence, a second contributor joining, multi-region landing, a
release defect traced to a forgotten bump, or a `bypass_actors` policy change).
These are now moot — the ADR is reopened and the automation shipped. They are
kept here only as the historical reasoning record.
