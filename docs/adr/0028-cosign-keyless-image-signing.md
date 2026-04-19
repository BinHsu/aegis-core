# ADR-0028: Cosign keyless image signing via Sigstore + OIDC

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-19                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4b "Sign & Scan" first mini-slice. Image artifacts in ECR today are unsigned — anyone with `ecr:PutImage` (the OIDC role, or hypothetically a compromised CI runner) can publish whatever they like under the legitimate tag. Cryptographic provenance closes the gap, and pairs with Phase 4b's downstream Trivy scan + admission verification work. |
| Related  | ADR-0025 (OCI packaging strategy), ADR-0026 v2 (model lifecycle / CI populator), ADR-0027 (frontend serving), `aegis-aws-landing-zone#83` (ECR resource policy defense-in-depth), `ARCHITECTURE.md` §10.1 (Supply Chain Integrity) |

## Context

Phase 4a shipped the full image release chain (rules_oci → CI smoke → CycloneDX SBOM → ECR push). The artifacts in ECR are byte-correct and traceable to a Git SHA, but **carry no cryptographic statement of authorship**:

- A compromised CI runner with the OIDC role's STS token could push a malicious image under a legitimate tag.
- Future ldz K8s admission (Phase 4c) has no signal to distinguish "image from our pipeline" vs "image someone with creds dropped into the same repo".
- ARCHITECTURE.md §10.1 promises Cosign-signed artifacts; that promise is now ready to be made real.

Phase 4b's "Sign & Scan" work is the umbrella for closing this. Cosign is the natural first piece — it's the lightest delta (one CI step per artifact) and unblocks the verification side (Kyverno admission policy at the K8s layer, cross-repo work with ldz).

## Decision

**Sign every image pushed to ECR using Sigstore Cosign keyless OIDC, store provenance in Rekor public transparency log, attach the gateway image's CycloneDX SBOM as a signed attestation.**

### Mechanism

1. Workflow installs Cosign via `sigstore/cosign-installer` (SHA-pinned, same discipline as other actions per `feedback_managed_over_diy.md`).
2. After each `oci_push` step, parse the manifest digest from the push output (`sha256:<hex>`).
3. `cosign sign --yes <repo>@<digest>` — uses the GitHub Actions OIDC token (already requested via `permissions: id-token: write` for the AWS OIDC step) to fetch an ephemeral signing cert from Sigstore Fulcio CA, signs the image manifest, records the signing event in Rekor transparency log.
4. For the gateway image only: `cosign attest --yes --predicate gateway.sbom.cdx.json --type cyclonedx <repo>@<digest>` — same keyless flow, but creates a CycloneDX-typed attestation linking the SBOM file to the image. Engine SBOM attestation is deferred until engine SBOM generation lands (ROADMAP follow-up mini-slice).
5. Signature artifacts and attestations are stored in the same ECR repo as additional OCI artifacts (Cosign suffixes the digest with `.sig` / `.att`). Existing `ecr:PutImage` permission covers — no new IAM ask to ldz.

### Cost

- **$0/month** — Sigstore Public Good Service (Fulcio CA + Rekor log) is donated infrastructure, free for any open-source / public artifact use.
- **~30s/CI-run** added latency: Fulcio cert fetch + signing + Rekor write. Negligible against the existing build + push timing.
- **No new IAM** asks: Cosign's writes go through the same `ecr:PutImage` family already granted.

### Verification path (deferred to ldz, cross-repo)

- aegis-core produces signatures + attestations.
- ldz wires Kyverno `verify-image` admission policy in EKS (their K8s manifest work, Phase 4c+) — admission rejects pods whose image references aren't signed by our GitHub Actions identity (matched via Fulcio cert subject + repo claim).
- aegis-core opens cross-repo issue requesting this; not blocking image push live today, just admission enforcement when Phase 4c lands.

## Why keyless over keyed

Two ways to sign with Cosign:

| Mechanism | Key management | Verification | Audit trail |
| --- | --- | --- | --- |
| **Keyed (`cosign generate-key-pair`)** | Long-lived signing key — must be stored as GH Secret, rotated periodically, never leaked. Public key distributed to verifiers. | `cosign verify --key <pub>` | None (verification is point-to-point) |
| **Keyless (Sigstore OIDC)** | None — signing identity is the GH Actions OIDC token claim (`repo:BinHsu/aegis-core:ref:refs/heads/main` + `job_workflow_ref`). Cert is ephemeral (~10 min), discarded after signing. | `cosign verify --certificate-identity-regexp ... --certificate-oidc-issuer https://token.actions.githubusercontent.com` | Public Rekor log entry — globally auditable, immutable |

Keyless wins on every dimension that matters here:

- **No key rotation** — signatures are tied to the OIDC identity at signing time, which is itself ephemeral. Rotation is automatic.
- **No leak risk** — there's nothing to leak. A compromised runner gets ~10 min of signing capability before the cert expires; with the runtime-only OIDC token already gated by `job_workflow_ref` (per ldz #79 Q4), the blast radius is small.
- **Public audit trail** — anyone (us, ldz, future contributors, security researchers) can `rekor-cli search` for our signatures. Tampering with the log requires breaking the merkle proof chain.
- **Verification config matches our existing trust scope** — the `repo:BinHsu/aegis-core:ref:refs/heads/main` claim that aegis-core's IAM trust policy already pins is the same claim Cosign records into the signature. Single source of truth for "what's authorized to sign."

Keyed signing's only advantage is offline verification (no Sigstore lookup needed). For our K8s admission case, the cluster has internet egress for everything else; one more lookup is noise.

## Consequences

### Positive

- ARCHITECTURE.md §10.1 "all container images and release binaries are signed with Cosign / Sigstore using GitHub Actions OIDC tokens" promise is half-live (signatures live; admission verification on ldz side, queued).
- Audit story is materially stronger — every image in ECR is now cryptographically tied to a specific GitHub Actions workflow run (which in turn is tied to a specific commit SHA on `main`).
- Pairs naturally with Slice 4a-2's CycloneDX SBOM — the SBOM is now a SIGNED attestation, not just a workflow artifact.
- Cross-repo unblock for ldz Phase 4c admission policy work.
- Portfolio-grade signal — Sigstore Cosign + Rekor is the canonical 2024+ SLSA L2 / supply-chain security implementation. Recruiters / security reviewers recognize it.

### Negative

- One more workflow step + one more action to maintain (`sigstore/cosign-installer` SHA pin); annual hygiene to bump.
- Sigstore Public Good Service depends on third-party donated infrastructure. Service-level risk is real but accepted (the entire OSS supply-chain ecosystem depends on it; if it goes down, we have bigger problems than aegis specifically).
- Engine SBOM attestation is asymmetric (gateway has, engine doesn't). Tracked as ROADMAP follow-up mini-slice; until it lands, engine images are SIGNED but not SBOM-attested.

### Out of scope (this ADR)

- SLSA Level 3 provenance (Phase 4b separate mini-slice — needs SLSA generator action like `slsa-framework/slsa-github-generator`, layered on top of Cosign signing).
- Trivy CVE scanning (Phase 4b separate mini-slice — `aquasecurity/trivy-action` against the pushed images).
- Kyverno admission verification (cross-repo to ldz; aegis-core delivers signed artifacts, ldz enforces).
- Rotating the Sigstore public-good services to a self-hosted Fulcio + Rekor (out of scope for the foreseeable future; only meaningful at very high signing volume / regulated environments).
- Frontend bundle signing (S3 objects per ADR-0027 — different signing model; revisit when CDN-distributed artifact signing becomes a Phase 4c+ concern).

## Cross-repo trail

- [`aegis-aws-landing-zone#96`](https://github.com/BinHsu/aegis-aws-landing-zone/issues/96) (cross-repo, filed in this slice) — request ldz to wire Kyverno `verify-image` admission policy in EKS staging when Phase 4c K8s manifests land. Verification config: cert subject regexp `^repo:BinHsu/aegis-core:ref:refs/heads/main$` (or fork-equivalent), OIDC issuer `https://token.actions.githubusercontent.com`.
