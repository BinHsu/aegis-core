# ADR-0027: Frontend serving via S3 + CloudFront, split subdomain

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-19                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4a Slice 5 ships the frontend deploy path. Frontend is a static React+Vite SPA bundle. The naive "Slice 5 = build a frontend OCI image" framing in earlier ROADMAP is wrong — the frontend is static assets that belong on a CDN, not a container. Three options (bundle into gateway image, separate frontend image with nginx, S3+CloudFront) were specced; ldz confirmed Option C was their long-standing roadmap. |
| Related  | ADR-0007 (Local mode LAN viewer flow — origin permissiveness rationale), ADR-0015 (hermetic Node.js via aspect_rules_js), ADR-0025 (OCI packaging strategy), `aegis-landing-zone-aws#90` (cross-repo Q&A confirming Option C), `aegis-landing-zone-aws#91` (provision trigger), ldz ADR-019 (forthcoming, frontend serving infra) |

## Context

aegis-core's frontend (`frontend_web/`) is a React + Vite SPA built as a static asset bundle to `frontend_web/dist/`. ADR-0002 commits the project to "pure web frontend" for the MVP horizon — Tauri remains an option for Phase 4+ but is not on the MVP critical path.

When Phase 4a packaging started, the original ROADMAP entry `Slice 4a-5: Frontend aegis-frontend image (static asset packaging)` framed the frontend as another OCI image to push to ECR. That's a category error — static assets don't need a container. Three real options:

| Option | aegis-core deliverable | ldz deliverable |
| --- | --- | --- |
| A. Bundle into gateway image | Gateway Go handler adds a `/` route serving `dist/`; image grows ~1MB | Nothing extra — gateway Deployment already deploys |
| B. Separate frontend image (nginx / caddy) | New OCI image with bundle baked in + nginx | Separate Deployment / Service / ingress route `/` → frontend, `/api/*` → gateway |
| C. S3 + CloudFront | CI workflow does `aws s3 sync dist/` to a bucket on every main push; CloudFront serves with edge caching | S3 bucket (OAC) + CloudFront distribution + ACM cert + Route53 + IAM role for OIDC push |

aegis-core's analysis: for demo horizon A is simplest (one Deployment, same origin trivially solves CORS); at scale C is best (edge caching, decoupled deploy cadence, cloud-native pattern); B is the awkward middle (extra K8s pod for static serving without CDN benefit).

Cross-repo question filed (ldz #90); ldz confirmed **Option C was the long-standing plan** — they had not yet provisioned only because there was no artifact to serve. ldz committed to a `staging/edge/` Terraservice (S3 bucket OAC-locked + CloudFront distribution + ACM cert in us-east-1 + Route53 record + new OIDC role `github-actions-aegis-core-frontend`) provisioned in ~3 hours when aegis-core's Slice 5 PR enters draft.

## Decision

**Option C: S3 + CloudFront, split subdomain, build-time env injection of API URL, gateway CORS allowlist.**

### Domain layout (binding, mirroring ldz #90 final)

| Hostname | Backend | Purpose |
| --- | --- | --- |
| `aegis-app.staging.binhsu.org` | CloudFront → S3 (frontend bucket) | Static SPA bundle |
| `aegis-api.staging.binhsu.org` | ALB → Gateway pod | gRPC, gRPC-Web, WebSocket (`/ws/viewer`), `/healthz` |
| `aegis-app.prod.binhsu.org` | (future) | Prod SPA when prod cut lands |
| `aegis-api.prod.binhsu.org` | (future) | Prod gateway |

Hyphenated `aegis-` prefix follows ldz's existing AWS account naming (`aegis-management`, `aegis-staging`, `aegis-prod`) — `aegis-app` parses unambiguously vs `aegisapp` (which reads as a single opaque word).

Path-based routing at the CDN layer (one host, `/` → frontend, `/api/*` → gateway) was rejected per ldz #90 because CloudFront's origin-group story doesn't cleanly handle WebSocket-grade upgrades on `/ws/*`, and ALB's session-affinity story is clearer when WebSocket terminates at ALB directly.

### Build-time env injection

The frontend already uses Vite's `import.meta.env.VITE_*` pattern for runtime configuration. The relevant existing env vars (per `frontend_web/src/lib/gateway-client.ts:44-52` and `frontend_web/src/pages/Host/HostPage.tsx:141`):

- `VITE_AEGIS_GATEWAY_ENDPOINT` — full URL the SPA opens for gRPC-Web + WebSocket connections
- `VITE_AEGIS_DEPLOY_MODE` — `"local"` | `"cloud"`

Slice 5 wires the staging release workflow to set both at build time:

```
VITE_AEGIS_GATEWAY_ENDPOINT=https://aegis-api.staging.binhsu.org
VITE_AEGIS_DEPLOY_MODE=cloud
```

The Vite build picks these up via `process.env` → `import.meta.env` substitution. Same-origin assumption (the existing fallback to `window.location.hostname:8080` in `gateway-client.ts:44-48`) is now WRONG for cloud mode (different subdomain) — must always set `VITE_AEGIS_GATEWAY_ENDPOINT` explicitly. The release workflow does this; LAN dev (Local mode) keeps the same-origin fallback.

### Gateway CORS allowlist

Same-origin fallback is gone in cloud mode; gateway must explicitly allow `https://aegis-app.staging.binhsu.org` for cross-origin gRPC-Web requests. Implemented in this slice via:

- New package `gateway_go/internal/cors/` — `Policy` type built from `AEGIS_ALLOWED_ORIGINS` env var (comma-separated)
- Empty / unset env → permissive (Local mode default; ADR-0007 LAN flow preserved)
- Non-empty env → strict allowlist; only listed origins pass; CORS header echoes the origin (with `Vary: Origin`) rather than `*`
- Used in BOTH `grpcweb.WithOriginFunc` (gRPC-Web) AND the `corsAllowed` HTTP handler (`/lan-ip`); same Policy instance, single source of truth
- Closes Phase 2 Known Gap from `ROADMAP.md:118` ("Cloud mode will tighten via a config-driven origin list")

Cloud-mode deployment sets `AEGIS_ALLOWED_ORIGINS=https://aegis-app.staging.binhsu.org` on the gateway pod's PodSpec env. Phase 4c K8s manifests carry this.

### CI workflow

New `.github/workflows/release-staging-frontend.yml`, modeled after `release-staging-image.yml`:

- Trigger: `push: branches: [main]` only (PRs validate via existing `Frontend live-browser smoke (Playwright)` job; pushes to S3 only on merged main)
- `paths` filter: only fires when `frontend_web/**` or this workflow itself changes
- Permissions: `id-token: write` + `contents: read` (matching the OIDC pattern from Slice 3)
- Steps: checkout → install deps via `tools/scripts/frontend.sh install` → build with env vars set → AWS OIDC assume-role → `aws s3 sync` → `aws cloudfront create-invalidation`

OIDC role: `github-actions-aegis-core-frontend` (provisioned by ldz per #90). Trust scope `repo:BinHsu/aegis-core:ref:refs/heads/main` + `job_workflow_ref` pin to this workflow file (same shape as `release-staging-image.yml` per ldz #79 Q4 IAM-condition pattern).

S3 bucket name + CloudFront distribution ID parameterized via GitHub Actions repository secrets (`AEGIS_FRONTEND_BUCKET_STAGING`, `AEGIS_FRONTEND_DISTRIBUTION_ID_STAGING`); ldz commits the values when provisioning lands.

## Why not the other options

### A (bundle into gateway image) — rejected

- Frontend changes (CSS tweak, copy edit) would require gateway image rebuild + redeploy → traffic shift on the API pod for a static-asset change. Heavyweight.
- No edge caching → every user paint hits the gateway pod, wasting K8s capacity for static-byte serving.
- Couples frontend release cadence to backend release cadence; canary on either is canary on both.
- Made sense as a "demo horizon shortcut", but ldz had already planned C, so committing to A would mean throwing it out at scale.

### B (separate frontend image + nginx) — rejected

- All the operational complexity of an extra K8s Deployment / Service / ingress route for static serving.
- No CDN edge caching → users far from the cluster region pay full RTT.
- The K8s-native crowd would call this "the right K8s pattern" but it gives up the actual benefit (CDN) for K8s aesthetic.
- Awkward middle ground — neither demo-simple (A) nor production-correct (C).

### C — chosen

- Edge caching is the real production win (TTFB ~10-30ms globally vs ~200ms+ from a single-region origin).
- Decoupled deploy cadence (frontend deploy = `aws s3 sync` + invalidate; gateway deploy = K8s rollout — no cross-blast-radius).
- Cloud-native pattern any infra reviewer recognizes.
- Cost: ~$0.50/month total (S3 storage tiny + CloudFront free tier covers low traffic).
- ldz already had it on roadmap; committing here unblocks parallel provisioning.

## Consequences

### Positive

- Frontend deploys atomic via `aws s3 sync` + cache invalidation — sub-minute end-to-end after CI build.
- Edge cache hides aegis from regional outages on the gateway path (static assets keep serving).
- Decoupled cadence lets us ship UI tweaks without touching gateway pod.
- Gateway CORS tightening closes a Phase 2 Known Gap that's been outstanding since the project started.

### Negative

- Cross-origin requires careful CORS configuration. Mistakes here surface as opaque "CORS error" in the browser dev console — annoying to debug. Mitigated by the strict-vs-permissive Policy split (Local mode unaffected; only Cloud mode tightens).
- CloudFront cache-invalidation has to fire on every deploy or users see stale assets. Workflow does this explicitly; missed invalidation is the #1 frontend deploy bug class.
- Two cross-repo coupling points (subdomain pin in CORS allowlist + Vite endpoint env var) — typo on either side breaks staging silently. Mitigated by integration-tests-against-staging in Phase 4c.
- Local mode's same-origin fallback in `gateway-client.ts:44-48` is now subtly misleading (works for Local mode only). Considered fixing it to require explicit env always; rejected for backward compatibility (existing LAN-viewer setups would break).

### Out of scope (this ADR)

- Prod cut domain provisioning (`aegis-app.prod.binhsu.org` / `aegis-api.prod.binhsu.org`) — handed off to a future cross-repo issue when prod-cut PR is in sight (per ldz #79 Q1).
- Multi-region frontend serving (CloudFront is multi-region by nature, but custom-domain DNS + ACM SAN list per region is a separate decision) — orthogonal to ldz #87 multi-region trigger.
- Frontend image OCI packaging — explicitly NOT happening; the original ROADMAP Slice 4a-5 entry is rewritten in this slice.
- WAF / Shield protection on CloudFront — defense-in-depth for Phase 5 hardening, not Phase 4a.
- Cache-control headers / cache-busting strategy for assets — Vite's content-hashed filenames cover the busting; cache-control headers tuning lives in CloudFront config (ldz side, ADR-019).

## GH Variables over hardcode / Secrets (revision 2026-04-19)

The first version of this ADR landed with workflow YAML referencing GitHub Secrets for the bucket / distribution-ID values (and an env-block for the role ARN). PR #35 then pivoted to hardcoded values across the env block, on the reasoning that AWS resource identifiers aren't credentials and shouldn't live in a write-only-readable vault. That second design surfaced a real cost: a forker would have to find/replace 5 touchpoints across 4 files to redirect deployment to their own AWS.

Reviewer (user) raised the forker concern + then proposed using **GitHub Repository Variables** (added 2023) as the right home for non-credential config. Variables are:
- Non-encrypted at rest (still scoped to repo, but readable from UI and `gh variable list` — debug-friendly)
- Forker-overridable in their fork's settings — same workflow YAML deploys to their AWS with zero code edits
- Named honestly: GitHub's "Secrets" feature is for credentials; "Variables" is for config

Final design: nine atomic Variables (`AWS_ACCOUNT_ID`, `AWS_REGION`, `ECR_REPO_NAME`, `ECR_PUSH_ROLE_NAME`, `FRONTEND_PUSH_ROLE_NAME`, `FRONTEND_S3_BUCKET`, `FRONTEND_CLOUDFRONT_DISTRIBUTION_ID`, `FRONTEND_DOMAIN`, `GATEWAY_DOMAIN`). Workflow `env:` blocks compose ARNs from atomic components (`arn:aws:iam::${{ vars.AWS_ACCOUNT_ID }}:role/${{ vars.FRONTEND_PUSH_ROLE_NAME }}`) so account-switching is a one-Variable change.

`oci_push.repository` in `packaging/{gateway,engine}/BUILD.bazel` keeps a hardcoded upstream default (rules_oci requires `repository` at BUILD-time analysis), but CI ALWAYS passes `--repository` at runtime sourced from Variables — forker's CI overrides via the flag, no BUILD edit needed.

Real secrets (`BUILDBUDDY_API_KEY` today; future Cosign signing keys) stay in GH Secrets. The Secrets-vs-Variables split now matches the real semantic split: credentials vs config.

The 9 Variables a forker sets, with a primary path (Terraform outputs from forked ldz, per ldz #93) and a fallback path (`aws cli` queries), were documented in a fork runbook that has since been removed as the architecture pivoted to GHCR public images (ADR-0023).

## Cross-repo trail

- ldz #90 (cross-repo Q&A): https://github.com/BinHsu/aegis-landing-zone-aws/issues/90
- ldz #91 (provision trigger): https://github.com/BinHsu/aegis-landing-zone-aws/issues/91
- ldz #88 (image set handoff — amended in this slice to clarify "no frontend image"): https://github.com/BinHsu/aegis-landing-zone-aws/issues/88
- ldz ADR-019 (forthcoming, frontend infra side; cross-link will be added when published)
