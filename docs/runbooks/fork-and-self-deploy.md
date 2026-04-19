# Runbook — Fork aegis-core and Deploy to Your Own AWS

| Field | Value |
| --- | --- |
| Audience | **Fork operator** who wants the deploy chain (ECR push, S3+CloudFront frontend) to land in their own AWS account, not the upstream's |
| Applies to | All Phase 4a release workflows (`release-staging-image.yml`, `release-staging-frontend.yml`) and the `oci_push` Bazel rules in `packaging/{gateway,engine}/BUILD.bazel` |
| Not applicable to | Casual cloners. Local `bazel build` + `pnpm dev` work end-to-end without any AWS — same hermetic story for everyone. This runbook only matters if you want CI to deploy somewhere. |
| Estimated time | ~30 minutes (5 min find/replace + provision time on the AWS side) |
| Cost | Whatever AWS charges in your account — for the demo-horizon footprint upstream uses, ~$1/month total (ECR + S3 + CloudFront + Route53). |
| Last reviewed | 2026-04-19 |

## Purpose

aegis-core's CI workflows hardcode AWS resource identifiers (account ID, role ARNs, S3 bucket names, CloudFront distribution IDs, ECR repo paths) at the workflow `env:` block / Bazel rule attribute level rather than via GitHub Actions secrets. That is the right call for the upstream operator (`BinHsu`) — values are debuggable from `git blame`, the workflow YAML is the truth, real secrets stay reserved for actual credentials. See ADR-0027 §"Why hardcode" and the matching commit on PR #35 for the detailed reasoning.

The cost of that decision is **fork friction**. A forker who runs the same CI in their fork will see workflows fail loudly at the OIDC step (`Not authorized to perform sts:AssumeRoleWithWebIdentity`) because the hardcoded role ARNs point at upstream's AWS account. This runbook is the explicit guide to fixing that — five touch points across four files.

## Prerequisites

- Forked both `BinHsu/aegis-core` and `BinHsu/aegis-aws-landing-zone` on GitHub.
- An AWS account you own (not upstream's `251774439261`).
- AWS CLI configured locally with admin access for the one-time bootstrap (you can later restrict).
- Terraform installed if you want to use `aegis-aws-landing-zone`'s IaC; otherwise you can hand-roll the resources.

## Step 0 — Decide your account ID

Pick (or note) **your** AWS account ID. Below referred to as `<YOUR_ACCOUNT>`. Find via:

```bash
aws sts get-caller-identity --query Account --output text
```

The remainder of this runbook treats `251774439261` as the upstream value to **search-and-replace** with `<YOUR_ACCOUNT>` everywhere it appears.

## Step 1 — Provision your AWS infra via the forked landing-zone

In your fork of `aegis-aws-landing-zone`:

1. Create a `terraform/environments/staging/` set targeting `<YOUR_ACCOUNT>` — the Terraform code already parameterizes account / region / domain via variables, so you just need to point them at your values.
2. Apply the `bootstrap` layer (creates the `aegis-core` ECR repo + the OIDC roles `github-actions-aegis-core-ecr` and `github-actions-aegis-core-frontend`).
3. Apply the `edge` layer if you want the frontend deploy chain (S3 bucket + CloudFront + ACM cert + Route53 record).
4. Note the four output values you will need in Step 2:
   - **ECR push role ARN**: `arn:aws:iam::<YOUR_ACCOUNT>:role/github-actions-aegis-core-ecr`
   - **Frontend deploy role ARN**: `arn:aws:iam::<YOUR_ACCOUNT>:role/github-actions-aegis-core-frontend`
   - **Frontend S3 bucket name** (default convention: `aegis-staging-frontend-<YOUR_ACCOUNT>`)
   - **CloudFront distribution ID** (opaque, e.g. `EXAMPLE0123456`)

If you also forked the domain off Cloudflare/your registrar, swap `binhsu.org` for your own domain in the Route53 + ACM resources.

## Step 2 — Find/replace in your aegis-core fork

Five touch points, four files. The values below are upstream's; replace with **yours** from Step 1.

### 2a. `.github/workflows/release-staging-image.yml`

```diff
-  ECR_ROLE_ARN: arn:aws:iam::251774439261:role/github-actions-aegis-core-ecr
+  ECR_ROLE_ARN: arn:aws:iam::<YOUR_ACCOUNT>:role/github-actions-aegis-core-ecr
```

(Search for `251774439261` in the file to catch any other references — there should be one in the env block.)

### 2b. `.github/workflows/release-staging-frontend.yml`

```diff
-  FRONTEND_ROLE_ARN: arn:aws:iam::251774439261:role/github-actions-aegis-core-frontend
-  FRONTEND_BUCKET: aegis-staging-frontend-251774439261
-  FRONTEND_DISTRIBUTION_ID: E5PYHGEEZQ7M8
-  VITE_AEGIS_GATEWAY_ENDPOINT: https://aegis-api.staging.binhsu.org
+  FRONTEND_ROLE_ARN: arn:aws:iam::<YOUR_ACCOUNT>:role/github-actions-aegis-core-frontend
+  FRONTEND_BUCKET: <your-frontend-bucket>
+  FRONTEND_DISTRIBUTION_ID: <your-distribution-id>
+  VITE_AEGIS_GATEWAY_ENDPOINT: https://aegis-api.<your-staging-subdomain>
```

### 2c. `packaging/gateway/BUILD.bazel`

```diff
 oci_push(
     name = "push_staging",
     ...
-    repository = "251774439261.dkr.ecr.eu-central-1.amazonaws.com/aegis-core",
+    repository = "<YOUR_ACCOUNT>.dkr.ecr.<your-region>.amazonaws.com/aegis-core",
     ...
 )
```

(Adjust the region too if you're not in `eu-central-1`.)

### 2d. `packaging/engine/BUILD.bazel`

Same pattern as 2c — same `repository` field, same value swap.

### 2e. (optional) `MODULE.bazel` distroless digest

Leave as-is unless you are mirroring `gcr.io/distroless/static-debian12` to a private registry. The digest pin is content-addressed and works from any registry that hosts the same image.

## Step 3 — Wire the IAM trust policies on your AWS side

The OIDC roles your forked `aegis-aws-landing-zone` provisioned have trust policies pinned to `repo:BinHsu/aegis-core:ref:refs/heads/main` AND a `job_workflow_ref` condition pinned to upstream's workflow file path. **Both must be updated** for your fork's CI to assume the role:

In your forked landing-zone, edit the OIDC role definitions to substitute:
- `repo:BinHsu/aegis-core:ref:refs/heads/main` → `repo:<YOUR_GITHUB_USER>/aegis-core:ref:refs/heads/main`
- `job_workflow_ref` value (if pinned) — same workflow filename, your repo prefix

Re-apply Terraform; the trust policy update is a one-line PR.

## Step 4 — (Optional) Disable the upstream-only paths in your fork

Two workflows are aware of `BUILDBUDDY_API_KEY` (Bazel remote cache). If you don't have a BuildBuddy account:

- Just leave the secret unset in your repo. The workflow already degrades gracefully — see [`docs/runbooks/buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) — and runs cold without remote caching.

## Verification

Push any commit to your fork's `main` branch. Both release workflows fire:

- `release-staging-image.yml` — should reach the OIDC step, get a token from `sts:AssumeRoleWithWebIdentity` against your role, push gateway + engine images to your ECR. Verify with:
  ```bash
  aws ecr describe-images \
    --repository-name aegis-core \
    --region <your-region> \
    --query 'imageDetails[].imageTags[]'
  ```
- `release-staging-frontend.yml` — should similarly reach OIDC, sync `dist/` to your S3 bucket, invalidate your CloudFront. Verify with:
  ```bash
  curl -vI https://<your-frontend-domain>/
  # → HTTP/2 200 from CloudFront
  ```

If OIDC still fails, the trust policy on your side likely still references upstream's repo. Walk back through Step 3.

## Why this runbook exists (the design honesty section)

aegis-core could have been built with all infra IDs as GitHub Secrets, making fork setup zero-edit (just create your own secrets). We didn't, and the trade-off is documented in ADR-0027:

- **For upstream operator**: secret-managed IDs would have made daily debugging awkward (can't read back from UI/CLI; cross-reference required). Hardcoded values are debuggable from `git blame` and the workflow YAML is the truth.
- **For fork operators**: the cost is this 30-minute setup. Honest one-time tax for a portfolio-grade repo where the upstream is the canonical case.

If the trade-off ever inverts (significant fork community materializes), the migration is mechanical — flip each `env:` reference back to `${{ secrets.NAME || 'upstream-default' }}`, remove the hardcoded values, and add a section on how upstream operator should set their own secrets. That PR would be ~50 lines of YAML.

## Related

- [ADR-0027 Frontend serving strategy](../adr/0027-frontend-serving-strategy.md) — the original design + the "why hardcode" rationale that PR #35 added.
- [ADR-0025 OCI packaging strategy](../adr/0025-oci-packaging-strategy.md) — Camp B doctrine + ECR push posture, which informs why the same hardcode-not-secret pattern applies to image release too.
- [`docs/runbooks/buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) — sister runbook for the optional BuildBuddy remote cache; also fork-aware.
- [aegis-aws-landing-zone](https://github.com/BinHsu/aegis-aws-landing-zone) — the Terraform repo whose `staging/{bootstrap,edge}/` Terraservices land the AWS resources this runbook references.
