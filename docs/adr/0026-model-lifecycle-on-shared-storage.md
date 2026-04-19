# ADR-0026: Model lifecycle on shared persistent storage (content-addressable)

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted (revised 2026-04-19)                                               |
| Date     | 2026-04-19                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4a Slice 4 ships the engine OCI image; Phase 4c will ship K8s manifests that mount model storage. Rolling deployments will run multiple engine pod versions concurrently, each potentially needing a different model version. The storage shape, the engine's loading contract, and the populator's identity must be designed together to avoid version coordination becoming an incident class. |
| Related  | ADR-0021 (shared ggml runtime), ADR-0025 (OCI packaging strategy), `aegis-aws-landing-zone#85` (cross-repo binding FYI), `models/manifest.json` schema |

## Revision (2026-04-19) — pivoted to content-addressable storage + CI-driven populator

The first version of this ADR (committed earlier the same day) proposed:
- **Filename-as-version flat layout** — `whisper-large-v3-turbo.q4.gguf` and `.q5.gguf` coexist in `/models/`
- **Engine SHA-verifies on startup** — engine reads file, recomputes SHA, compares to manifest
- **Storage population via Kubernetes Job** — ldz-owned Job runs `download_models.sh`
- **Reference-counted prune Job** — periodic ldz-owned Job removes unreferenced files

Reviewer pushed three improvements that fundamentally simplify the design:

1. **Pivot to content-addressable storage (CAS)**: filename = SHA256 of the content. `<bucket>/<model-name>/<sha256-hex>.<ext>` — directly removes the entire "is this file corrupted?" investigation class. Filename being the hash means existence-at-path = correctness by construction.
2. **CI populates instead of pod / Job**: GitHub Actions in `.github/workflows/release-staging-image.yml` walks the manifest at build time, HEAD-checks each (model, sha) in S3, pulls + pushes anything missing. Engine pod stays maximally read-only; failure to populate surfaces in CI before any artifact ships.
3. **Defer pruning entirely**: ~$0.16/month per accumulated GB of retired models is noise vs the operational cost + risk of running a reference-counting prune Job. Revisit only if accumulated cost becomes meaningful (>$10/month, ~50GB of retired models — years away).

This revision rewrites the ADR end-to-end against the new model. Any reader looking at git blame for the prior version should treat this revision as superseding.

## Context

Slice 4 deliberately keeps models out of the engine OCI image (~50-100 MB image vs ~1.5 GB if baked in; ADR-0025 §"Slice 4 distroless variant decision"). Engine reads `/models` at runtime from a Kubernetes-mounted directory backed by ldz-provisioned storage. **Storage realization: Amazon S3 Files** (April 2026 launch, EFS-backed S3 bucket-as-filesystem; cross-repo FYI to ldz at [aegis-aws-landing-zone#85](https://github.com/BinHsu/aegis-aws-landing-zone/issues/85)). RD owns the architectural call; ldz executes; discussion space is at the IAM / resource / lifecycle mechanics layer.

The follow-on architectural question that this ADR resolves: **what happens during a rolling deployment** when:

- Old engine pods (still serving traffic) need model **version V1**;
- New engine pods rolling out need model **version V2**;
- Both pods mount the **same `/models` directory**.

Naive in-place upgrade would SIGBUS old pods' `mmap`. The chosen design eliminates the entire failure class via content addressing.

## Decision

**Content-addressable storage (CAS) layout + CI-driven populator + read-only engine + deferred pruning.**

### Storage layout (binding)

```
<bucket-prefix>/<model-name>/<sha256-hex>.<ext>
```

Concrete examples:
```
aegis-staging-models-251774439261/whisper-large-v3-turbo-q4/a3f8b2c1...d4e5.gguf
aegis-staging-models-251774439261/whisper-large-v3-turbo-q4/9b7e3f2a...8d1c.gguf  ← different version of same model
aegis-staging-models-251774439261/bge-m3-Q4_K_M/c4d5e6f7...8a9b.gguf
```

Properties this gives us for free:
- **Multi-version coexistence is structurally correct**: different SHAs → different paths → no collision possible.
- **Storage corruption is a path-existence question, not a content question**: file at `<sha>.gguf` either exists with that exact content (because filename IS the content's hash), or it doesn't. There is no "wrong content at right path" failure mode.
- **Audit / debugging is trivial**: `aws s3 ls` shows the full version history of every model.
- **Operational analogy is well-understood**: same as Docker layer storage, git object store, Sigstore Rekor — well-trodden pattern.

### S3 storage tier (binding)

**S3 Standard** for the underlying bucket. S3 Files' EFS-backed cache layer delivers ~1ms latency for active data regardless of bucket tier; paying for Express One Zone ($0.16/GB/month vs $0.023) buys nothing for our access pattern (rare cold reads, dominant warm-cache reads). Cost: ~2GB × $0.023 = ~$0.05/month for current model set.

### Region scope (binding for now)

**Single region: `eu-central-1`** for Phase 4a / Phase 4c. When ldz lands multi-region deployment infrastructure (per ldz #79's mention of Karpenter cross-region capability), this ADR's region scope will be re-spec'd. Two future options to evaluate then:
- **Per-region buckets, CI populates each**: simplest, no replication state, slightly more bandwidth at populate time
- **S3 Cross-Region Replication (CRR)**: async replicate from primary to secondary, more $$, simpler operational model

Premature to commit either today. ldz multi-region work is the trigger.

### Responsibilities — three-way split

#### Engine responsibilities (read-only consumer)

1. **Bundle the manifest.** Engine OCI image embeds `models/manifest.json`. Each entry encodes `(model_name, sha256, size, ext)` — together they uniquely identify the file and its expected location.

2. **Walk the manifest at startup, before serving traffic.** For each entry where `"required": true`:
   - Compute path: `${AEGIS_MODEL_PATH:-/models}/<model_name>/<sha256>.<ext>`
   - HEAD-check: file exists, size matches manifest's expected bytes
   - **Trust by construction** — do NOT recompute SHA at startup. Filename IS the SHA; if file exists at that path, content is correct by CAS invariant. (Saves ~10s/GB of unnecessary hashing.)
   - If any required file missing or wrong-size: fail fast with operator-readable diagnostic (model_name, expected sha, expected size, actual size, expected path)
   - Only after all required entries pass, start the gRPC server.

3. **Pure read-only.** IRSA permission scope is `s3:GetObject` on the model bucket prefix. Engine code has zero S3-write paths. K8s mount is `readOnly: true` (defense-in-depth — IAM is the real layer).

4. **Tolerate "extra" objects.** Other models / future versions / SHAs belonging to concurrently-running engine versions MUST not cause this engine to refuse to start. The walk is "do my required entries exist with correct size?", not "is the bucket exactly what I expect?".

#### CI responsibilities (single writer, idempotent populator)

1. **HEAD-first populator.** `.github/workflows/release-staging-image.yml` (the same workflow that pushes engine OCI images on `push: branches: [main]`) extends with a "Populate model storage" step. For each manifest entry:
   - HEAD `<bucket>/<model_name>/<sha256>.<ext>`
   - If 200: skip (already populated; cost = ~ms HEAD request)
   - If 404: pull from upstream URL per manifest, verify downloaded SHA matches expected, push to S3

2. **Single writer guarantee.** CI workflow runs are serialized per branch by GitHub Actions. Even if two main commits land back-to-back, their CI runs queue. No race conditions, no distributed lock needed.

3. **Failure in CI, not in production.** If upstream is down or returns wrong content, the CI step fails and the engine image push is also aborted (or we accept a temporarily-broken-but-rare lag). Either way, broken state surfaces during build, never in production.

4. **IAM extension (cross-repo with ldz)**: existing OIDC role `github-actions-aegis-core-ecr` extends with `s3:HeadObject + s3:GetObject + s3:PutObject` on the model bucket prefix. Same OIDC trust scope (refs/heads/main + job_workflow_ref pin to release-staging-image.yml) applies; no new auth surface.

#### Storage / infra responsibilities (ldz)

1. **Provision the S3 bucket** named `aegis-staging-models-251774439261` (extending ldz's existing naming convention `aegis-<env>-<purpose>-<account-id>`). S3 Standard storage class. Single region eu-central-1.

2. **Wire S3 Files file-system handle** to the bucket so K8s pods can mount it.

3. **Extend the engine IRSA role** (`aegis-staging-aegis-engine`, already provisioned per ldz #11) with the read-only S3 perm:
   ```
   { Effect: Allow, Action: ["s3:GetObject"], Resource: "arn:aws:s3:::aegis-staging-models-251774439261/*" }
   ```

4. **Extend the OIDC role** (`github-actions-aegis-core-ecr`) with the populator perms:
   ```
   { Effect: Allow, Action: ["s3:HeadObject", "s3:GetObject", "s3:PutObject"], Resource: "arn:aws:s3:::aegis-staging-models-251774439261/*" }
   ```

5. **No prune Job today** — see "Pruning deferred" below.

### Pruning — deferred

The first version of this ADR specified a reference-counted prune Job. That has been **explicitly deferred** because:

- Storage cost math: ~1GB per retired model SHA × ~2 retired SHAs/year = ~2GB accumulated/year × $0.023/GB/month = **~$0.05/month** added per year of accumulation. Reaches $1/month after ~20 years.
- Operational cost: a periodic Job that walks active pods, reads each manifest, computes union, deletes unreferenced — non-trivial K8s logic with real failure modes (race conditions during deploy, pod-list staleness, accidental over-prune).
- Risk: accidentally deleting a SHA still referenced by a slow-rolling-back deployment causes pod startup failure — operational incident.

Trade ratio is overwhelmingly against pruning at this scale. Revisit when accumulated retired-model storage exceeds $10/month (~50GB; years away). Before then, the bucket grows monotonically; that is a feature, not a bug — old SHAs remain instantly available for rollback / forensics.

## Why CAS over previous "filename-as-version" proposal

| Property | Filename-as-version (rejected) | CAS (chosen) |
| --- | --- | --- |
| Multi-version coexistence | Naming convention discipline | Structural — different SHA = different path |
| SHA verification cost | Recompute on every startup (~10s/GB) | Zero (filename IS the SHA) |
| "Wrong content at right path" failure mode | Real (manifest-vs-file divergence) | Impossible by construction |
| Operator investigation complexity | High ("which is wrong: file or manifest?") | Low ("file missing or right-size present") |
| Self-healing potential | Risky (engine writing back is auth expansion) | CI populates as build-time guarantee |
| Operational analogy | Custom convention | Docker registry / git / Sigstore — well-trodden |

## Why CI populator over pod self-recovery / sidecar Job

| Property | Pod self-recovery (rejected) | Sidecar Job (rejected) | CI populator (chosen) |
| --- | --- | --- | --- |
| Engine IAM surface | RW (pod needs s3:PutObject) | R (pod stays read-only) | **R only (smallest blast radius)** |
| First-pod cold start | Slow (~5min pull) | Slow (sidecar sync) | **Instant (CI pre-populated)** |
| Race control | Multi-pod racing on same SHA | Sidecar coordinator | **Single writer per build (free)** |
| Failure surface | Production runtime | Production runtime | **CI build time (broken artifact never ships)** |
| Operational pieces ldz maintains | runtime auto-recovery monitoring | sidecar Helm chart | **CI step + IAM (already have both)** |

## Consequences

### Positive

- Rolling deployment is structurally correct (different SHA → different path → never collide).
- Engine code is minimal: HEAD-check + size-check + trust + start gRPC. No SHA recompute, no S3 write logic.
- CI populator is naturally idempotent (HEAD-first). Steady-state cost = ~250ms / build for HEAD requests.
- Pull failure (HuggingFace down, wrong-SHA upstream) surfaces in CI, never in production.
- "Who's the fool?" investigation class disappears — file-at-path is correct or doesn't exist; no third option.
- Storage growth is bounded by accumulated SHA count; cost stays sub-dollar for years.

### Negative

- CI workflow gets a new step. Steady-state cost ~250ms (HEAD-only); cost on new SHA push ~3-5min (one-time per SHA, then HEAD-only forever).
- IAM surface expands for the OIDC role (gain `s3:HeadObject + s3:PutObject`). Trust scope unchanged (still refs/heads/main + job_workflow_ref pin); only the actions broaden, not the principal.
- Disaster recovery scenarios that bypass CI (manual cluster recreation) need a break-glass populator Job. Documented in `docs/runbooks/model-storage-disaster-recovery.md` (Phase 4c stub).
- Multi-region story is deferred. When ldz multi-region deploy lands, this ADR re-revises with the chosen replication model.

### Out of scope (this ADR)

- Engine implementation details: HEAD-check vs F_OK / fstat, CLI vs config-file for AEGIS_MODEL_PATH, etc. — Phase 4c implementation discretion.
- The CI populator's exact Bazel / shell / Python implementation — Phase 4a-4 follow-up commit will add the YAML step + supporting logic; same OIDC + IAM model already in use.
- Lifecycle / Glacier policy — deferred per "Pruning deferred" section.
- Multi-region storage replication — deferred until ldz multi-region deploy is real.
- "Engine startup manifest validation" code change in `engine_cpp/cmd/engine/main.cc` — tracked in ROADMAP Phase 4c.

## Cross-repo trail

- ldz #82 (closed, superseded) — original storage requirement issue with 3 amendments; design evolution preserved
- ldz #85 (open, `cross-repo/fyi`) — binding FYI; will be amended with this v2 spec in the same session
- ldz #83 (open, cross-repo) — ECR resource policy defense-in-depth (orthogonal but related supply-chain control)
