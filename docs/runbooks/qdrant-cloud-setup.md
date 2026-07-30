# Runbook — Qdrant Cloud Free-Tier Signup

| Field | Value |
| --- | --- |
| Audience | **Upstream repo operator** (required for staging / demo deployments); **fork operator** (optional, only if fork wants its own managed Qdrant). |
| Applies to | ADR-0020 + Phase 3b / Phase 4 deployment (managed vector DB for demo horizon, before self-hosted Qdrant on EKS is justified by real usage). |
| Not applicable to | Casual cloners and developers running local integration tests — follow [`qdrant-local-setup.md`](qdrant-local-setup.md) instead. No cloud signup is needed to build, run unit tests, or run local integration tests. |
| Estimated time | 5–10 minutes |
| Cost | $0 — Qdrant Cloud free tier (1 GB storage, 1 node, eu-central-1 or us-east-1 available at signup). Enough for demo usage; not suitable for production workloads. |
| Last reviewed | 2026-04-17 |

## Purpose

Provision a managed Qdrant cluster on Qdrant Cloud free tier, wire
its API key into the `aegis-core` GitHub Actions secrets, and
record the endpoint for the (forthcoming) `engine --seed
--target=cloud` path.

Same structural shape as
[`buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) — signup
+ key + GHA secret. This is the Qdrant analogue: Qdrant Inc. owns
the SaaS; we do NOT provision through `aegis-landing-zone-aws`
because Qdrant Cloud is not an AWS-managed service (it runs on
AWS but is operated by Qdrant).

This runbook is **for demo horizon only**. When self-hosted Qdrant
on EKS becomes justified (Phase 4+ cost / latency tuning, or a
compliance ask for "data must stay in our VPC"), a sibling runbook
will document the ldz-provisioned path. Until then, Qdrant Cloud is
the right answer because the user's 2026-04-17 cost stance rules
out always-on EKS compute for demo-horizon concerns.

## Prerequisites

- A GitHub account with admin access to the `aegis-core` repository
  (to create a repo-scoped GitHub Actions secret). For upstream this
  is `BinHsu`.
- A browser session signed into that GitHub account.

Nothing else.

## Step 1 — Sign up for Qdrant Cloud

1. Open <https://cloud.qdrant.io/>.
2. Click **"Sign up with GitHub"**. GitHub OAuth requests read-only
   profile + email access; it does NOT request repo scopes.
3. Authorize the Qdrant Cloud OAuth app.
4. On the onboarding screen, confirm you are on the **Free** plan
   (dashboard header should NOT show a "Standard" / "Premium" badge).

After this step you have an empty Qdrant Cloud org with no clusters.

## Step 2 — Create a free-tier cluster

1. On the cluster list page, click **"Create cluster"** (button may
   also read **"New cluster"** depending on UI version).
2. Fill in the form:
   - **Provider**: AWS.
   - **Region**: `eu-central-1` if the aegis-core staging account
     (ldz #54: `251774439261`) is in `eu-central-1` — same-region
     keeps latency + egress cost minimal. Otherwise pick the region
     matching your deployment.
   - **Cluster name**: `aegis-staging` (match the ldz cluster name
     convention; Qdrant Cloud's naming is local to its org).
   - **Plan**: **Free** (1 GB storage, 1 node).
   - Accept Qdrant's TOS checkbox.
3. Click **"Create"**. Provisioning typically takes ~30–60 seconds.
4. Once the cluster shows **"Running"**, copy the **gRPC endpoint**
   from the cluster details page. Format:
   `<cluster-id>.eu-central-1.aws.cloud.qdrant.io:6334`. Save this
   for Step 4.

## Step 3 — Create an API key scoped to the cluster

1. From the cluster details page, click **"Access management"** →
   **"API Keys"** (path may vary slightly between UI versions).
2. Click **"Create API Key"**.
3. Fill in the form:
   - **Name**: `aegis-core-staging-ci` — shows up in Qdrant Cloud's
     audit log.
   - **Scope**: the cluster you just created. Do NOT create a global
     key — the free tier likely only supports one cluster, but
     scope-as-narrow-as-possible is the standing policy (mirrors the
     dedicated-role least-privilege stance from ADR-0014 §δ).
   - **Access**: **Read + Write** (CI needs to upsert seed points
     AND search; a read-only key blocks `engine --seed`).
4. Click **"Create"**. The key is displayed **once**. Copy the entire
   string to your clipboard — format is a long opaque base64-ish
   token.

## Step 4 — Add the endpoint + key to GitHub Actions secrets

1. Open
   <https://github.com/BinHsu/aegis-core/settings/secrets/actions>.
   (Click path: repo → **Settings** → left sidebar **Security** →
   **Secrets and variables** → **Actions** → **Secrets** tab.)
2. Click **"New repository secret"** twice, once for each of:
   - Name: `QDRANT_URL` — Value: the gRPC endpoint from Step 2, with
     the `https://` scheme prefix (e.g.,
     `https://<cluster-id>.eu-central-1.aws.cloud.qdrant.io:6334`).
     QdrantClient's `ConfigFromEnv()` parses the scheme to decide
     whether to enable TLS — `https://` enables it, which Qdrant
     Cloud requires.
   - Name: `QDRANT_API_KEY` — Value: the API key from Step 3.
3. Both secrets now appear under "Repository secrets" with values
   redacted.

For fork operators, the URL path becomes
`https://github.com/<your-username>/aegis-core/settings/...` — the
rest is the same.

## Verification

After a CI workflow lands that consumes `QDRANT_URL` + `QDRANT_API_KEY`
(Slice 6+ — `engine --seed --target=cloud` run in CI, not yet
wired), you should see:

- Successful `engine --seed` run in the GitHub Actions log.
- A populated collection in the Qdrant Cloud cluster details page,
  visible under the **Collections** tab with the chunk count + storage
  footprint.
- No `UNAUTHENTICATED` / `PERMISSION_DENIED` gRPC errors in the log.

Until that wiring lands, verification is manual: run the integration
test locally pointing at the cloud cluster instead of a local
Qdrant:

```bash
QDRANT_URL=https://<cluster-id>.eu-central-1.aws.cloud.qdrant.io:6334 \
QDRANT_API_KEY=<paste-your-key> \
  ./tools/bazelisk/bazelisk test \
    //engine_cpp/tests/integration:qdrant_client_test \
    --test_env=QDRANT_URL \
    --test_env=QDRANT_API_KEY \
    --test_output=all
```

All four cases should PASS against the cloud cluster (the test
creates a unique collection per run and does not clean up — the
free-tier 1 GB ceiling means you should periodically delete old
`aegis_test_*_collection` entries via the Qdrant Cloud UI).

## Rotation

Qdrant Cloud API keys do not auto-expire. Rotate manually:

- **Every 180 days** (routine hygiene).
- **Immediately** on any suspected exposure (key leaked in a log,
  former contributor needs revocation, key committed by accident).

Rotation procedure (zero-downtime):

1. In Access management → API Keys, click **"Create API Key"** with
   a fresh name like `aegis-core-staging-ci-2026Q3`.
2. Copy the new key.
3. Update the `QDRANT_API_KEY` secret at
   <https://github.com/BinHsu/aegis-core/settings/secrets/actions>.
4. Let one CI run complete green to confirm the new key works.
5. Delete the **old** key in Qdrant Cloud.

## Revocation (emergency)

If the key is known-compromised:

1. In Access management → API Keys, find the old key and click
   **"Revoke"** / trash icon. The key is invalidated server-side
   immediately.
2. Create a new key (Step 3 above).
3. Update the GHA secret (Step 4 above).

## Cost ceilings to watch

Free tier is 1 GB storage + 1 node. The Taiwan corpus bundled at
`docs/rag/taiwan.md` chunks to a few thousand ~450-char pieces ×
1024-dim bge-m3 embeddings × 4 bytes per float = ~a few MB per
corpus, well under the ceiling. If the demo scales to multiple
corpora + test runs over time, periodically delete old
`aegis_test_*_collection` collections via the cluster UI to stay
under the ceiling.

## Related

- [ADR-0020](../adr/0020-engine-owns-inference.md) — why we use
  Qdrant specifically (vs OpenSearch / pgvector / Bedrock).
- [`docs/runbooks/qdrant-local-setup.md`](qdrant-local-setup.md) —
  the local-dev counterpart for running Qdrant on your machine.
- [`proto/qdrant/v1.17.1/PROVENANCE.md`](../../proto/qdrant/v1.17.1/PROVENANCE.md)
  — the client-side proto pin that must stay in sync with the
  server version you run in Qdrant Cloud.
