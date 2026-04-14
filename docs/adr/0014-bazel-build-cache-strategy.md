# ADR-0014: Bazel Build Cache Strategy

- **Status**: Accepted (decision **deferred to Phase 4**; this ADR
  freezes the trade-off analysis and the trigger conditions so the
  Phase 4 reviewer does not have to re-derive them)
- **Date**: 2026-04-13
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

## Decision Outcome

**Phase 1–3: Option α.** Keep `actions/cache` as the only mitigation.
The cold-build pain is bearable at solo-dev scale.

**Phase 4 decision deferred — re-evaluate when ANY of these
**triggers** fires:**

| # | Trigger condition                                      | Likely Phase 4 choice |
|---|--------------------------------------------------------|-----------------------|
| T1 | Second active contributor joins (≥10 PRs/month from a non-owner author) | β (BuildBuddy) — cache reuse value scales with producers |
| T2 | Bazel cache > 10 GB → `actions/cache` overflow         | β or γ — α stops working entirely; pick by T3 below |
| T3 | First enterprise customer with security review demanding "cache must not leave our VPC" | γ (self-hosted) — third-party cache is a no-go |
| T4 | CI cold-build cost > 30 min/PR consistently            | β preferred; γ acceptable |
| T5 | Phase 4a OCI image build needs cross-PR layer caching  | β (cleaner), γ (acceptable) |
| T6 | None of T1–T5 by Phase 5 launch                        | Stay on α permanently — cost was lower than anticipated |

Whoever lands the Phase 4 decision MUST update this ADR with the
final choice and link the resolving commit.

### Why Decision Now Would Be Premature

- Phase 1 has a single contributor on a single dev box. Remote cache
  reuse value is exactly zero except for CI.
- `actions/cache` empirically works (last run hit and saved 13 min).
- Free-tier BuildBuddy or a t4g.small `bazel-remote` are both <1 hr
  setup at any future point — no architectural lock-in.
- Choosing wrong now (e.g., BuildBuddy SaaS that we later need to
  rip out because of T3) is a multi-week unwind including supply-
  chain re-attestation. Choosing later is a same-day swap.

### What This ADR Locks In

- The trade-off space is closed — Phase 4 reviewer does not need to
  re-survey.
- The triggers (T1–T6) are the agreed switching criteria. A new
  trigger requires this ADR to be updated, not merely a side
  decision.
- Whatever cache we adopt, the chain `MODULE.bazel pin → SHA-verified
  download → cache-served artifact → SLSA provenance` MUST remain
  intact. The cache is a delivery optimization, not a source of
  truth.

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
  fallback to γ (self-hosted) is still cheap because the option
  space is documented.
- **GitHub `actions/cache` 10 GB cap.** The most likely T2 trigger.
  Phase 4a OCI artifact caching alone may push past it; budget
  monitoring should be wired into CI summaries before the cap bites.

## Related

- ADR-0009 C++ Build and Toolchain — original mention of "Phase 4
  remote cache" as cold-build mitigation.
- ARCH §10.1 Supply Chain Integrity — the SBOM / SLSA / Cosign
  framework any cache choice must preserve.
- ROADMAP Phase 4 — this ADR is the source of truth for the cache
  decision; ROADMAP carries a one-line pointer.
- `.github/workflows/ci-baseline.yml` — current `actions/cache`
  configuration is the Option α implementation.
