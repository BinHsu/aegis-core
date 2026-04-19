# Runbook — Fork aegis-core and Deploy to Your Own AWS

| Field | Value |
| --- | --- |
| Audience | **Fork operator** who wants the deploy chain (ECR push, S3+CloudFront frontend) to land in their own AWS account, not the upstream's |
| Applies to | All Phase 4a release workflows (`release-staging-image.yml`, `release-staging-frontend.yml`) |
| Not applicable to | Casual cloners. Local `bazel build` + `pnpm dev` work end-to-end without any AWS — no setup. This runbook only matters if you want CI to deploy somewhere. |
| Estimated time | ~30 minutes (10 min setting GH Variables + provision time on the AWS side) |
| Cost | Whatever AWS charges in your account — for the demo-horizon footprint upstream uses, ~$1/month total (ECR + S3 + CloudFront + Route53). |
| Last reviewed | 2026-04-19 |

## Purpose

aegis-core's release workflows source AWS infra identifiers (account ID, role names, S3 bucket name, CloudFront distribution ID, region, domains) from **GitHub Repository Variables**, not from hardcoded values or GitHub Secrets. This is the design fork-friendliness rests on: a fork operator overrides the Variables in their fork's repo settings, and the same workflow YAML deploys to their AWS account with **zero code edits**. ADR-0027 §"GH Variables over hardcode/Secrets" documents the rationale.

The work for a forker is therefore:
1. Provision the AWS infra in their own account (via forked `aegis-aws-landing-zone` Terraform — strongly recommended)
2. Read the values out of Terraform outputs (or AWS CLI as fallback)
3. Set 9 Repository Variables in their forked aegis-core
4. Push to `main` and watch the workflows land green

## Prerequisites

- Forked both `BinHsu/aegis-core` and `BinHsu/aegis-aws-landing-zone` on GitHub.
- An AWS account you own (not upstream's `251774439261`).
- AWS CLI configured locally with admin (or at least sufficient IAM/S3/CloudFront/ECR read perms) for the one-time bootstrap.
- Terraform installed if using the recommended primary path.
- `gh` CLI authenticated to your GitHub account, or browser access to your fork's Settings.

## Step 1 — Provision AWS infra via your forked landing-zone

In your fork of `aegis-aws-landing-zone`:

1. Edit `terraform/environments/staging/` (or your equivalent) to target **your** AWS account / region / domain. The Terraform code parameterizes these via variables.
2. Apply the `bootstrap` layer — provisions:
   - ECR repo `aegis-core`
   - OIDC role `github-actions-aegis-core-ecr` with trust policy bound to `repo:<YOUR_GITHUB_USER>/aegis-core:ref:refs/heads/main`
   - OIDC role `github-actions-aegis-core-cache` (for Bazel remote cache, optional)
3. Apply the `edge` layer — provisions:
   - S3 bucket for frontend (default name: `aegis-staging-frontend-<YOUR_ACCOUNT>`)
   - CloudFront distribution
   - ACM certificate (in `us-east-1`)
   - Route53 record for `aegis-app.<your-staging-subdomain>`
   - OIDC role `github-actions-aegis-core-frontend` with trust scope to your fork

4. **Update both OIDC roles' trust policies** so the `sub` condition is your repo (not `BinHsu/aegis-core`) and `job_workflow_ref` (if the trust policy pins it) targets the corresponding workflow file in your repo. Your forked Terraform should accept both as variables — review before applying.

## Step 2 — Read the values you'll plug into GitHub Variables

### Primary path — Terraform outputs (preferred)

If your fork of `aegis-aws-landing-zone` declares outputs for the resources (per aegis-aws-landing-zone#93 once landed), this is one command:

```bash
cd /path/to/your/aegis-aws-landing-zone/terraform/environments/staging
terraform output -json > /tmp/aegis-staging-outputs.json
```

Pull the values you need from the JSON:

```bash
jq -r .aegis_core_ecr_role_arn.value      /tmp/aegis-staging-outputs.json
jq -r .frontend_push_role_arn.value       /tmp/aegis-staging-outputs.json
jq -r .frontend_s3_bucket_name.value      /tmp/aegis-staging-outputs.json
jq -r .frontend_cloudfront_distribution_id.value /tmp/aegis-staging-outputs.json
jq -r .frontend_alternate_domain_name.value /tmp/aegis-staging-outputs.json
```

(Output names listed match aegis-core's request in ldz issue #93. Adjust if your fork renames them.)

### Fallback path — AWS CLI queries (if outputs aren't declared yet)

For each value, the AWS CLI command from a normal AWS user's perspective (assumes you have read perms in the account):

| Value you need | AWS CLI command | Where it lives |
| --- | --- | --- |
| AWS account ID | `aws sts get-caller-identity --query Account --output text` | Your STS / IAM tokens |
| AWS region | (you chose it; e.g. `eu-central-1`) | Your decision |
| ECR repo name | `aws ecr describe-repositories --query 'repositories[?contains(repositoryName, \`aegis-core\`)].repositoryName' --output text` | ECR console / CLI |
| ECR push role name | `aws iam list-roles --query 'Roles[?contains(RoleName, \`aegis-core-ecr\`)].RoleName' --output text` | IAM console / CLI |
| Frontend push role name | `aws iam list-roles --query 'Roles[?contains(RoleName, \`aegis-core-frontend\`)].RoleName' --output text` | IAM console / CLI |
| Frontend S3 bucket | `aws s3api list-buckets --query 'Buckets[?contains(Name, \`aegis-staging-frontend\`)].Name' --output text` | S3 console / CLI |
| Frontend CloudFront ID | `aws cloudfront list-distributions --query 'DistributionList.Items[?Aliases.Items[?contains(@, \`aegis-app.\`)]].Id' --output text` | CloudFront console / CLI |
| Frontend domain | (you chose the subdomain in Step 1) | Your DNS provider / Route53 zone |
| Gateway domain | (you chose the subdomain in Step 1; not yet provisioned by ldz until Phase 4c) | Your DNS provider / Route53 zone |

If you used the AWS Console to provision (no Terraform), find each in the corresponding service's console.

## Step 3 — Set the 9 GitHub Repository Variables

Either via the web UI at `https://github.com/<YOUR_GITHUB_USER>/aegis-core/settings/variables/actions`, or via `gh`:

```bash
cd /path/to/your/aegis-core
gh variable set AWS_ACCOUNT_ID                       --body "<your-account-id>"
gh variable set AWS_REGION                            --body "<your-region>"
gh variable set ECR_REPO_NAME                         --body "aegis-core"
gh variable set ECR_PUSH_ROLE_NAME                    --body "<your-ecr-role-name>"
gh variable set FRONTEND_PUSH_ROLE_NAME               --body "<your-frontend-role-name>"
gh variable set FRONTEND_S3_BUCKET                    --body "<your-frontend-bucket>"
gh variable set FRONTEND_CLOUDFRONT_DISTRIBUTION_ID   --body "<your-distribution-id>"
gh variable set FRONTEND_DOMAIN                       --body "<your-frontend-subdomain>"
gh variable set GATEWAY_DOMAIN                        --body "<your-gateway-subdomain>"
```

Verify with:

```bash
gh variable list
```

(Variables are non-encrypted and readable from both UI and CLI — that's the design. Use Secrets only for actual credentials.)

## Step 4 — Push to main and verify

Any commit to your fork's `main` triggers the relevant release workflow(s) (or use `workflow_dispatch` from the GH Actions UI for a clean test).

Expected outcome:

- **`release-staging-image.yml`** — pushes gateway + engine images to your ECR. Verify:
  ```bash
  aws ecr describe-images \
    --repository-name aegis-core \
    --region <your-region> \
    --query 'imageDetails[].imageTags[]'
  ```
- **`release-staging-frontend.yml`** — syncs `frontend_web/dist/` to your S3 bucket, invalidates CloudFront. Verify:
  ```bash
  curl -vI https://<your-frontend-domain>/
  # → HTTP/2 200 from CloudFront
  ```

If OIDC fails (`Not authorized to perform sts:AssumeRoleWithWebIdentity`), the trust policy on your AWS side still references `BinHsu/aegis-core`. Walk back to Step 1.4.

## Why this design (forker-friendliness via Variables, not hardcode)

aegis-core's earlier design hardcoded these values in workflow YAML / `BUILD.bazel` files. That worked but cost forkers a 5-touchpoint find/replace across 4 files. The migration to GitHub Variables (PR #35, ADR-0027 §"GH Variables over hardcode/Secrets") makes fork setup zero-code-edit at the cost of needing this runbook to know what to set.

GitHub Secrets were considered as the home for these values but rejected:
- Secrets are write-only-readable from human paths (UI / `gh secret list` shows names only) — debugging requires runtime CI inspection
- Naming is misleading: bucket names + role ARNs aren't credentials, they're config
- Variables (added 2023) are the correct GitHub feature for non-credential repository config: encrypted at rest, readable from UI/CLI for ops debugging

Real secrets (BUILDBUDDY_API_KEY, future Cosign signing keys) stay in GitHub Secrets where they belong.

## Related

- [ADR-0027 Frontend serving strategy](../adr/0027-frontend-serving-strategy.md) — the canonical design + the GH Variables decision rationale.
- [ADR-0025 OCI packaging strategy](../adr/0025-oci-packaging-strategy.md) — Camp B doctrine + ECR push posture.
- [`docs/runbooks/buildbuddy-cache-setup.md`](buildbuddy-cache-setup.md) — sister runbook for the optional BuildBuddy remote cache; also fork-aware.
- [aegis-aws-landing-zone](https://github.com/BinHsu/aegis-aws-landing-zone) — the Terraform repo whose `staging/{bootstrap,edge}/` Terraservices land the AWS resources this runbook references; ldz #93 tracks the Terraform outputs request that makes Step 2 (primary path) one command.
