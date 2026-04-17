# ADR-0014: Bazel Build Cache Strategy

- **Status**: Accepted — two-phase plan chosen on 2026-04-17
  (Option β BuildBuddy Personal for the **demo horizon**, Option δ
  S3 + credential_helper + GHA OIDC for **production**)
- **Date**: 2026-04-13 (original trade-off); 2026-04-17 (decision landed)
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Cold Bazel builds in Aegis Core take ~15 minutes on `ubuntu-latest`
(per the first run of the `bazel-unit-tests` CI job in commit
`21dedf9`) because every PR fetches and compiles the full transitive
graph: whisper.cpp + ggml + grpc-cpp + protobuf + abseil + boringssl
+ apple_support shim. Incremental local builds are sub-second, but CI
runners are ephemeral and do not persist Bazel's `--output_user_root`
across runs by default.

ADR-0009 §"Cons of (a) and mitigations" already named "remote cache
in Phase 4" as the answer, but it never specified **which** remote
cache, **when** we would adopt it, or **what cost** we accept until
then. This ADR closes those three gaps — by writing down the
analysis now (when the trade-offs are fresh) and explicitly deferring
the choice to Phase 4 (when we will have the data to make it).

The current cold-build cost is paid by exactly two consumers:

1. **CI** on every push to `main` and on every PR (mitigated
   partially by `actions/cache` keyed on `MODULE.bazel`,
   `.bazelversion`, `.bazelrc` — see
   `.github/workflows/ci-baseline.yml`).
2. **Fresh clones** for new contributors. CLAUDE.md Rule 6 + the
   bazelisk wrapper guarantee no global pollution but offer no
   reuse of artifacts produced by other contributors.

## Decision Drivers

- **D1. Don't pay until it hurts.** Phase 1 is solo-dev. CI runs at a
  rate of ~10 PRs / week. `actions/cache` already covers the warm
  case; cold-cache events happen roughly twice per week. Adopting a
  remote cache today would incur infrastructure / operational cost
  for a problem that does not yet bite.
- **D2. Keep the door open.** Bazel remote cache is opt-in via four
  lines in `.bazelrc`. Any of the candidate solutions can be wired in
  in <1 hour once the decision is made; we do not need to design for
  it now beyond keeping `MODULE.bazel` declarative (already true).
- **D3. Supply chain integrity.** Whatever cache we choose must not
  weaken ARCH §10.1's SLSA / Cosign / SBOM story. A compromised cache
  serving back malicious artifacts would be a nuclear option for an
  attacker. SaaS providers must have credible SOC 2; self-hosted
  must run inside our trust boundary (i.e., the
  aegis-aws-landing-zone repo's VPC).
- **D4. Solo-dev → small-team transition.** The trigger to adopt
  remote cache is almost certainly "second contributor joins" —
  cache reuse value scales with the number of cache producers, not
  the number of build minutes saved per individual run.
- **D5. Public OSS repo.** Aegis is public (Phase 0 §0.5 visibility
  decision). BuildBuddy SaaS offers a free tier for public repos; the
  same isn't true for many alternatives.

## Considered Options

### Option α — Do nothing; rely on `actions/cache` only

Status quo. CI uses GitHub Actions's built-in cache to store
`.bazel_cache/` keyed on the dependency manifest. Cold runs hit when
the dep manifest changes (Dependabot bumps, our own version
upgrades).

- **Pros**: zero infra cost, zero ops, zero supply-chain new
  surface. Already implemented as of `8c523e2`.
- **Cons**: cap of 10 GB per repo on GitHub free / OSS plans —
  Aegis's full Bazel cache will eventually exceed this once Phase 4
  brings rules_oci + rules_rust + the frontend bundle. Cache
  invalidation is coarse (any MODULE.bazel change blows the entire
  cache). No reuse across contributors' local machines.

### Option β — BuildBuddy SaaS (free OSS tier)

`https://buildbuddy.io` runs a managed Bazel remote cache + remote
execution backend with a free plan for public repositories. Setup is
~5 lines in `.bazelrc`:

```
build --bes_results_url=https://app.buildbuddy.io/invocation/
build --bes_backend=grpcs://remote.buildbuddy.io
build --remote_cache=grpcs://remote.buildbuddy.io
build --remote_timeout=3600
build --remote_header=x-buildbuddy-api-key=$BB_KEY
```

- **Pros**: production-grade infra; SOC 2 Type II certified;
  invocation dashboards (timing flame graphs, dep graph, test result
  history) come along for free; integrates with GitHub OAuth so
  team-member access is one click.
- **Cons**: introduces a third-party producer of build artifacts —
  ARCH §10.1 SLSA L3 chain now includes BuildBuddy as a transitive
  trust root; need to audit. Free tier has soft limits the docs do
  not publish; paying tier is $X/seat which becomes nontrivial at
  team scale. Network egress on every build action.

### Option γ — Self-hosted `bazel-remote` on AWS

`https://github.com/buchgr/bazel-remote` is a single-binary HTTP/2
cache backed by S3 or local disk. Run on a t4g.small in the
`aegis-aws-landing-zone` repo's VPC; point Bazel at it via:

```
build --remote_cache=https://cache.aegis.internal
build --remote_upload_local_results=true
```

- **Pros**: artifacts never leave our trust boundary; cost is purely
  EC2 + S3 — predictable; no per-seat licensing; full control over
  retention / eviction policy.
- **Cons**: ops surface — single point of failure unless we run
  multi-AZ; certificate management; observability is rolled-our-own;
  total ownership of "is the cache poisoned?" question. The
  `aegis-aws-landing-zone` repo already runs ArgoCD + monitoring so adding one
  more service is 1 day of work, not 1 week, but it IS work.
  **Also**: requires an always-on EC2 instance, which contradicts the
  2026-04-17 cost stance that EKS (and by extension any always-on AWS
  compute dedicated to cache) is off the table until real users
  justify it.

### Option δ — S3 direct via Bazel 7.4+ `--credential_helper` + GHA OIDC

Added 2026-04-17 after a trade-off walk-through that originally
treated S3 as needing a proxy layer (Lambda / Fargate). Bazel 7.4+
introduced `--credential_helper`, which lets Bazel itself invoke a
small external script to sign each HTTP cache request. With SigV4
headers produced by that helper, Bazel can speak directly to S3's
HTTP cache without any compute in the middle. The auth chain is:

```
GitHub Actions OIDC ── aws-actions/configure-aws-credentials ──►
  short-lived IAM creds (~1 hour) ──► Bazel --credential_helper
  signs each PUT/GET with SigV4 ──► S3 HTTP cache bucket
```

Local developers do not see or need this. The cache flag lives in the
CI workflow's command-line invocation, not in the committed
`.bazelrc` (see `.bazelrc:90` — `try-import %workspace%/.bazelrc.user`
pattern). `clone → bazel build` stays hermetic and remote-cache-free.

- **Pros**:
  - **Zero always-on compute on our side.** S3 is intrinsically
    always-on, managed by AWS.
  - **Full zero-trust.** OIDC federation → ephemeral IAM creds → no
    long-lived secret stored in GHA. Aligns with the AWS OIDC trust
    chain the `aegis-aws-landing-zone` repo will build anyway for
    Cosign / ECR push / EKS deploy (Phase 4 packaging).
  - **Cost is predictable and small.** At 100 GB/month transfer:
    ~$1 storage + ~$9 egress ≈ **~$10/month**. An order of magnitude
    below γ's EC2 + ops overhead.
  - **Portfolio signal.** Demonstrates AWS OIDC federation + Bazel's
    `--credential_helper` mechanism — both are non-obvious infra
    skills worth showing.
- **Cons**:
  - **Setup has moving parts.** A credential helper script
    (~50 lines of Go or a shell wrapping `aws s3 presign`), an OIDC
    trust policy for `aegis-core`'s repo in IAM, and a CI workflow
    block with `permissions: id-token: write`. None of these are
    hard, but together it's ~½ day of first-time wiring.
  - **Requires `aegis-aws-landing-zone` to own the S3 bucket and IAM
    role**, so depends on that repo being in a state where Phase 4
    infrastructure is actually deployable.
  - **Observability is basic.** S3 access logs + CloudWatch metrics
    vs. BuildBuddy's rich invocation dashboards. For cache-hit-rate
    debugging this is enough; for build-graph forensics, less so.

## Decision Outcome

**Two-phase plan, landed 2026-04-17:**

### Phase A — demo horizon: **Option β (BuildBuddy Personal)**

For the window between "actively developing Phase 3b–4 with
accelerating CI pain" and "the `aegis-aws-landing-zone` repo has
real deployed infrastructure including IAM OIDC for GHA".

Why β now:
- Free tier (100 GB cache transfer / month, up to 80 remote-build
  cores) covers projected usage with headroom.
- Zero ops — sign up, copy one API key into a GitHub Actions
  secret, add five lines to the CI workflow, done.
- Unblocks the "cold CI build takes 15 min" pain immediately.
- API-key auth is acceptable in this window: the repo is public,
  the cache fronts public code, and the key scope is limited to
  one org namespace.

### Phase B — production: **Option δ (S3 direct + OIDC)**

Triggered when ANY of these conditions fires:

| # | Trigger condition                                                   |
|---|---------------------------------------------------------------------|
| T1 | `aegis-aws-landing-zone` ships the AWS OIDC trust policy for GHA (for Cosign / ECR / EKS deploy). At that point the S3 cache is a +1 trust-policy line, not a new project. |
| T2 | BuildBuddy free-tier limits start to bite (>100 GB/month transfer observed in BuildBuddy dashboard for ≥2 consecutive months) |
| T3 | First external contributor lands a PR and their build participates in cache reuse (validates that δ's IAM model works for non-owner identities) |
| T4 | BuildBuddy free-tier policy shifts (pricing change, TOS change, region restriction) — fallback posture is "we already know where we're going" |
| T5 | A compliance / customer review demands "build artifacts must not leave customer-controlled infrastructure" — δ's "our S3 bucket, our IAM" makes this a trivial yes |

None of these are hard deadlines; T1 is the most likely first-mover.
When it fires, the β→δ migration is a same-day swap: keep the S3
bucket provisioned ahead of time (cheap), swap the workflow's
`--remote_cache=grpcs://remote.buildbuddy.io` for the S3 URL + add
the `--credential_helper` flag, pull the BuildBuddy key out of GHA
secrets. Total blast radius: one workflow file.

### δ prerequisites — what `aegis-aws-landing-zone` must provide

The β→δ migration requires a cross-repo coordination step per
README §"Cross-Repository Coordination Protocol" (`README.md:466–496`).
When the migration trigger fires, the next action from `aegis-core`
is to file a `cross-repo/blocking` issue on `aegis-aws-landing-zone`
requesting the following — **do not self-migrate before the sibling
side has provisioned these resources**:

#### 1. Dedicated IAM role (strict least privilege)

- **Role name**: `github-actions-aegis-core`
- **Does NOT share identity** with any Terraform-execution role, any
  `aws-landing-zone-admin` role, or any broad-scope role. Fresh role,
  fresh trust policy.
- **Does NOT carry `AdministratorAccess`** or any other managed
  policy. The only policy attached is the bucket-scoped inline
  policy below.

#### 2. S3 permissions, scoped to the cache bucket only

Inline policy attached to the role above. Nothing broader.

```json
{
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"],
      "Resource": "arn:aws:s3:::<cache-bucket>/*"
    },
    {
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::<cache-bucket>"
    }
  ]
}
```

- **NO `s3:*`** wildcards.
- **NO cross-bucket access.**
- **NO IAM write actions** (`iam:PutRolePolicy`, `iam:PassRole`, etc.).
- **NO KMS write actions** beyond `kms:Decrypt` / `kms:Encrypt` if
  the bucket uses a customer-managed key (add only if needed).

#### 3. OIDC trust policy

Restrict the `sub` claim to `aegis-core`'s `main` branch for
write-capable identity. Whether PR builds get write access (and
thus can poison the cache from a forked PR) is a **TBD decision at
migration time**; the safe default is PR builds assume a read-only
sibling role. Initial trust policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::<account>:oidc-provider/token.actions.githubusercontent.com"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {"token.actions.githubusercontent.com:aud": "sts.amazonaws.com"},
      "StringLike": {"token.actions.githubusercontent.com:sub": "repo:BinHsu/aegis-core:ref:refs/heads/main"}
    }
  }]
}
```

#### 4. S3 bucket configuration (ldz-Terraform-owned)

- **Lifecycle rule**: evict objects after 7–14 days (cache is
  ephemeral; a week covers typical PR review windows).
- **Public access**: blocked at every layer (BPA on account + bucket).
- **Versioning**: OFF (cache is ephemeral; versioning just bloats
  storage and obscures the lifecycle rule's intent).
- **Encryption**: SSE-S3 minimum; SSE-KMS acceptable if ldz's
  default posture uses customer-managed keys.
- **Request Payer**: `BucketOwner` (ldz pays egress; explicit is
  better than cross-account ambiguity).

#### 5. Rationale for this least-privilege posture

A compromised `aegis-core` CI job that somehow exfiltrates the
assumed role's short-lived credentials should be limited to **cache
tampering** — which `actions/cache` as the fallback layer plus the
SHA-verified download pins in `MODULE.bazel` will still catch at
build time. A shared role with Terraform-grade AWS access would
turn the same compromise into full AWS account escalation. The
per-repo dedicated role is the forcing function that keeps blast
radius bounded to the cache tier.

### δ prerequisites — implementation status (2026-04-17)

Cross-repo coordination resolved same-day via
[landing-zone #72](https://github.com/BinHsu/aegis-aws-landing-zone/issues/72)
(cross-repo/blocking) → ldz PR #74 merged to main.

Provisioned via Terraform (code merged; live ARNs available after
the next `terraform-apply-baseline.yml` run on ldz side):

| Resource | Name | Terraform output |
| --- | --- | --- |
| ECR push role | `github-actions-aegis-core-ecr` | `aegis_core_ecr_role_arn` |
| S3 cache role | `github-actions-aegis-core-cache` | `aegis_core_cache_role_arn` |
| S3 cache bucket | `aegis-staging-bazel-cache-251774439261` | `bazel_cache_bucket_name` |

Trust policy and inline policies implemented exactly per the spec
above for both roles. The `github-actions-terraform` role's trust
policy no longer references any `repo:BinHsu/aegis-core:*` subject
(one accidental re-addition in PR #73 was reverted before merge).

### Accepted deviation from §4 bucket config

**Versioning**: this ADR requested OFF; ldz PR #74 chose ON.

**Accepted.** Cost delta is on the order of $1–2/month for a
cache-scale bucket (cache keys are content-addressable SHA-prefixed,
so the working set does not accumulate many historical versions —
lifecycle rule expires noncurrent versions along with current on
the same 14-day window). Correctness is not impacted. Raising a
follow-up coordination round to flip this one bit would cost more
in round-trip time than the deviation costs per year. The lesson for
future spec authors: pin the bits that affect correctness and
security tightly; leave "preference" bits (cost, posture parity) as
guidance unless the delta justifies another round of coordination.

### Phase 4d β→δ migration — what's left after this

The migration from Option β (BuildBuddy Personal) to Option δ (S3 +
OIDC) is now purely an aegis-core-side CI workflow change. The
landing-zone infrastructure is pre-provisioned and will be live
after ldz's next Terraform apply. When the migration trigger fires
(most likely T1 — `aegis-aws-landing-zone` shipping its AWS OIDC
trust policy for Cosign / ECR / EKS deploy, which this PR #74
effectively satisfies), the aegis-core side needs:

- `permissions: id-token: write` in the `bazel-unit-tests` job
- `aws-actions/configure-aws-credentials` action step with
  `role-to-assume: <aegis_core_cache_role_arn from ldz outputs>`
- A small credential helper script under `tools/scripts/` that
  signs S3 HTTP requests with SigV4 using the assumed role's
  short-lived creds
- Swap `--remote_cache=grpcs://remote.buildbuddy.io` for the S3
  HTTP cache URL (`https://<bucket>.s3.<region>.amazonaws.com` with
  `--credential_helper` pointing at the signer script)
- Pull the `BUILDBUDDY_API_KEY` secret from the workflow
- Delete the old BuildBuddy API key at
  <https://app.buildbuddy.io/settings/org/api-keys>

Total blast radius: one workflow file + one new script. Estimated
wiring time: half a day once the trigger fires.

### What α and γ become

- **Option α** (`actions/cache` only) is retained as the
  **no-internet fallback**: if both BuildBuddy and S3 are unreachable
  in the same CI run, `actions/cache` still delivers the warm case.
  The existing `actions/cache` block in `ci-baseline.yml` is NOT
  removed when β / δ land — it's the second layer.
- **Option γ** is **set aside**. Always-on EC2 for a cache tier
  contradicts the 2026-04-17 cost stance ("EKS not always-on"), and
  δ delivers the same trust-boundary property (our VPC / our account)
  without the compute bill.

### What This ADR Locks In

- The trade-off space is closed — future reviewers do not re-survey.
- The β→δ migration path is pre-committed; no fresh analysis needed
  when T1–T5 fires.
- Whatever cache serves a given run, the chain `MODULE.bazel pin →
  SHA-verified download → cache-served artifact → SLSA provenance`
  MUST remain intact. The cache is a delivery optimization, not a
  source of truth.
- `clone → bazel build` local-only workflow is sacrosanct: remote
  cache flags never land in the committed `.bazelrc`, only in CI
  invocations or developer-local `.bazelrc.user`.

## Consequences

### Positive

- ROADMAP Phase 4 stops accumulating "what about remote cache?"
  comments — ADR has the answer.
- Trade-off analysis is captured while context is fresh; the Phase 4
  reviewer is unlikely to be the same person who initially built
  the system.
- Trigger-based decision pattern means we will not adopt remote
  cache prematurely (ops cost) or too late (developer
  productivity tax).

### Negative

- A future reviewer might want to revisit the trade-off because
  the SaaS / self-hosted landscape shifted (e.g., BuildBuddy
  changes pricing; Earthly / Nx Cloud become viable). They would
  need to update this ADR rather than write fresh analysis. That
  is the correct flow — but does require discipline.
- Recording the analysis now and not adopting can read as
  "documentation theatre" if no trigger ever fires (T6 outcome).
  Acceptable cost — the analysis is on the order of one engineer-
  hour, the alternative is rediscovering it under deadline
  pressure.

### Risks

- **BuildBuddy free tier policy change.** Their TOS could shift; the
  fallback is δ (S3 direct), documented and a same-day swap (see
  the β→δ trigger table and T4 specifically).
- **GitHub `actions/cache` 10 GB cap.** Handled by β / δ covering the
  primary hot path; `actions/cache` remains as the no-internet
  fallback layer, so its cap is no longer the forcing function.
- **Bazel `--credential_helper` protocol drift.** Shipping feature in
  Bazel 7.4+; protocol is small but could evolve. Mitigation: pin
  `.bazelversion` (already done — `7.4.1`), and keep the credential
  helper script in the repo under `tools/scripts/` so upgrades are a
  same-PR change.

## Related

- ADR-0009 C++ Build and Toolchain — original mention of "Phase 4
  remote cache" as cold-build mitigation.
- ARCH §10.1 Supply Chain Integrity — the SBOM / SLSA / Cosign
  framework any cache choice must preserve.
- ROADMAP Phase 4 — this ADR is the source of truth for the cache
  decision; ROADMAP carries a one-line pointer.
- `.github/workflows/ci-baseline.yml` — current `actions/cache`
  configuration is the Option α implementation.
