# Aegis Core — Threat Model

<!-- session-close-review: trust-surface list — only if session added a new external dep / SaaS / storage tier -->

- **Status**: Draft (STRIDE skeleton for MVP / Phase 1–4)
- **Last reviewed**: 2026-04-11
- **Scope**: Aegis Core application architecture. Infrastructure-level
  threats (EKS cluster, AWS account, network boundary) are covered by
  the [aegis-aws-landing-zone](https://github.com/BinHsu/aegis-aws-landing-zone)
  repository's own threat model.

## How to Use This Document

This is a living document. Every architectural change that introduces a
new trust boundary, processing stage, storage mechanism, or external
integration **MUST** update this threat model. Reviewers of such PRs
MUST verify the threat model is still accurate.

The model uses **STRIDE** (Spoofing, Tampering, Repudiation, Information
disclosure, Denial of service, Elevation of privilege) as the taxonomy,
and **attacker profiles** to scope realistic threats.

## System Overview

For the full data flow, see `ARCHITECTURE.md` §4. Briefly:

- **Host** (staff machine) captures audio via web APIs (`getUserMedia` /
  `getDisplayMedia`) and streams over WebRTC to the Go Gateway.
- **Go Gateway** is a stateless fan-out relay that forwards PCM to the
  C++ engine and fans transcript segments to viewers.
- **C++ Engine** runs whisper.cpp and anonymous speaker diarization.
  Holds audio only in process RAM; performs no voiceprint matching
  and no biometric processing of any kind (ADR-0012).
- **Viewers** (the boss and other observers) join via short-lived JWT
  invite links and receive live transcript in a rolling window.
- **Durable stores** hold only tenant metadata, auth records, consent
  ledger, and RAG corpora — **never** meeting content.
- **External SaaS trust surface (staging Cloud deploy, 2026-04
  onward):** AWS Cognito User Pool + Hosted UI — `custom:tenant_id`
  claim drives ADR-0022 RAG isolation. Qdrant Cloud free tier — EU
  region `eu-central-1` for GDPR boundary; TLS gRPC on port 6334 with
  `api-key` metadata-header auth. Both credentials flow via
  ldz-managed SSM Parameter Store → External Secrets Operator → K8s
  Secret; gateway and engine pods consume via standard `secretKeyRef`
  — no aegis-core code path reads SSM directly. Trust-surface delta
  from the 2026-04-17 threat model: add two SaaS endpoints to the
  "durable stores" row; no new attacker profile (A5 supply-chain
  already covers third-party SaaS compromise).
- **Continuous-profiling egress (ADR-0035, 2026-05-17):** the gateway
  gains an optional outbound connection to a Grafana Cloud Pyroscope
  ingest endpoint, configured via `AEGIS_PYROSCOPE_ENDPOINT`. The data
  on this channel is operational telemetry only — CPU / allocation /
  goroutine stack samples (function names + line numbers from
  aegis-core's own Go code), asset class 9 ("operational logs and
  metrics; should contain no content"). No PCM, transcript, or token
  material crosses it. Trust-surface delta: one more egress SaaS
  endpoint of the same class as the OTLP-trace exporter; A5
  supply-chain already covers the `pyroscope-go` client dependency and
  the SaaS endpoint. The channel is fail-soft and disabled by default
  (empty endpoint = no-op), so it is absent from the trust surface
  entirely until the landing-zone provisions ingest and the env var is
  set.

## Assets to Protect

Listed in descending order of value / sensitivity:

1. **Audio PCM** (transient, but disclosure is severe).
2. **Transcript content** (can contain business secrets, personal data).
3. **Prompter / RAG output** (tactical business advice, reveals
   strategy).
4. **Consent ledger** (evidentiary artifact for privacy-policy
   agreement).
5. **Session tokens** (invite JWTs; grant viewer access).
6. **User credentials and Cognito JWTs**.
7. **RAG corpus** (persistent knowledge base; business-sensitive).
8. **Tenant metadata** (account, billing, config).
9. **Operational logs and metrics** (should contain no content).

**Note**: voiceprint / biometric data does NOT appear on this list
because Aegis does not process it — see ADR-0012. This is the single
biggest simplification to the threat model: the highest-risk asset
class that was formerly at the top of the list has been structurally
removed, not merely mitigated.

## Attacker Profiles

### A1 — External Unauthenticated Attacker

Someone on the public internet with no Aegis credentials. Can probe
public endpoints, spray credentials, scan for known CVEs.

### A2 — External Authenticated Attacker (Tenant User, Hostile)

A legitimate Aegis user who actively tries to exceed their authorized
access — cross-tenant data access, privilege escalation, extracting
other users' transcripts, RAG corpora, or account data.

### A3 — Compromised Session Viewer (Leaked Invite Link)

Someone who obtained a session join URL via social engineering,
accidental paste to the wrong Slack channel, or interception of
unencrypted messaging. Has the JWT for the session's lifetime but
no Aegis account.

### A4 — Insider (Aegis Operator)

An engineer or SRE with access to infrastructure who attempts to
access meeting content out of curiosity, malice, or coercion.

### A5 — Malicious Dependency / Supply Chain

A compromised third-party library (npm, Cargo crate, Go module,
C++ vendored lib), base container image, or build tool. The attacker
has no direct access but places code inside the build.

### A6 — On-Device Attacker (Local Mode)

An attacker with physical or administrative access to the host
laptop running Local mode. They can read process memory, dump RAM,
install kernel-level capture tools.

### A7 — Network Adversary on Same LAN (Local Mode)

An attacker on the same Wi-Fi network in Local mode who can attempt
to reach the host's LAN-bound Go Gateway port.

## STRIDE Analysis

### Spoofing

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| S1 | Attacker (A1) claims to be a legitimate host and creates meetings | `CreateMeeting` endpoint | Cognito JWT validation on host auth middleware; Local mode uses dummy local auth | Low |
| S2 | Attacker (A3) joins a meeting they were not intended to view | Viewer join endpoint | Session token verification (ADR-0001); short token lifetime; out-of-band secure sharing recommended | Medium — link leakage is the primary risk |
| S3 | Host process spoofs another host's session ID | Go Gateway session registry | Session IDs are random 128-bit, bound to the creator's JWT | Low |
| S4 | Malicious C++ engine spoofs transcript on behalf of another tenant | gRPC ingest stream | mTLS between Go GW and C++ Engine (Istio, §8); Tier 2/3 pods are shared but namespace-isolated | Low |
| S5 | On-device attacker (A6) impersonates the legitimate host by reading session token from browser storage | Local mode | Out of threat model — A6 has physical device access; local mode's trust boundary is the OS user account | Accepted |

### Tampering

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| T1 | Attacker modifies transcript segments in flight | Go Gateway → Viewer path | gRPC-Web / WebSocket over TLS (Cloud); `ws://` on LAN (Local) | Medium on LAN — A7 could MITM if Wi-Fi is unencrypted |
| T2 | Malicious model file replaces legitimate whisper.cpp weights | C++ engine | Model manifest SHA256 + PGP attestation (§10.1); loader refuses unverified files | Low |
| T3 | Compromised dependency (A5) injects code into production build | Any binary | SBOM, Cosign signing, SLSA provenance, dependency pinning, license scan (§10.1). **Live as of Phase 4b mini-slice 1**: (a) container base image (`gcr.io/distroless/static-debian12:nonroot`) pinned by SHA256 manifest-list digest in `MODULE.bazel` `oci.pull` (ADR-0025); image runs as `nonroot` (uid 65532) under read-only rootfs, validated by mandatory CI smoke; (b) CycloneDX SBOM emitted per gateway-image build via `anchore/sbom-action` SHA-pinned (Phase 4a-2); (c) Sigstore Cosign keyless signing of gateway + engine OCI manifests using GitHub OIDC → Fulcio CA → Rekor public transparency log; gateway SBOM attached as a signed Cosign attestation (ADR-0028). SLSA L3 provenance, Trivy CVE scan, ldz-side Kyverno verify-image admission still queued (Phase 4b remaining mini-slices + ldz #96). | Medium — supply chain is never fully solvable; new transitive trust on `gcr.io/distroless` registry availability + image-publisher integrity (digest pinning bounds the latter) + Sigstore Public Good Service availability for verification (donated infrastructure) |
| T4 | Debug audio dump flag enabled in production | C++ engine | ADR-0005 R7: dump code is compile-time stripped (`#ifdef`), not runtime-toggleable | Very low — requires deploying a dev build to prod |
| T5 | Attacker tampers with consent ledger to fake consent | DynamoDB (Cloud) / SQLite (Local) | Append-only schema; Cloud: DynamoDB Streams → S3 WORM bucket for tamper-evident backup | Low |
| T6 | Attacker alters `/models/manifest.json` to match a malicious model's hash | `/models` directory | Manifest itself is PGP-signed (§10.1); loader verifies signature before trusting manifest | Low |
| T7 | Leaked `AEGIS_CORE_DEPLOY_PAT` lets an attacker open and auto-merge a PR carrying arbitrary K8s manifests into `aegis-core-deploy` `main`, which ArgoCD then reconciles onto the cluster | `aegis-core-deploy` repo / `release-staging-image.yml` `bump-image-tag` job | PAT is **fine-grained**, scoped to the single `aegis-core-deploy` repo with `contents: write` + `pull-requests: write` (no org, no other repos), and expiring; stored as a GitHub Actions repository secret, masked in logs (`::add-mask::`). The `pull-requests: write` scope is required because the bump job opens a PR via `gh pr create` and arms auto-merge via `gh pr merge --auto` rather than pushing directly to `main`. `aegis-core-deploy` `main` is **PR-gated** by an active ruleset (PR required, squash-only, required `CodeRabbit` status check, review-thread resolution) — so the attack vector is abuse of the PR / auto-merge path, not an unconstrained direct push: a leaked PAT can open a bump PR and arm auto-merge, which still merges only after the required check passes. The compensating control is that the *images* the manifests point at are still Cosign-signed + SLSA-attested (T3), and ldz-side Kyverno verify-image admission (queued) rejects unsigned images regardless of what a tampered manifest references. Defence-in-depth gap: a tampered manifest could still alter non-image fields (resources, env, NetworkPolicy). | Medium — bounded by PAT scope + expiry + the PR / required-check gate + image-signature enforcement |

### Repudiation

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| R1 | User denies consenting to real-time audio processing | Legal / regulatory (GDPR general PII rules) | Append-only consent ledger with `user_id`, `policy_version`, `timestamp`, `client_metadata` (§9.3); DynamoDB Streams → S3 WORM in Cloud mode for tamper-evident backup | Low |
| R2 | User denies creating a meeting or performing an action | Go Gateway audit log | Structured audit log with request ID + tenant ID + user ID + action; retained per §10 | Low |
| R3 | Aegis operator denies accessing customer data | Infrastructure logs | EKS Pod Identity audit trail via AWS CloudTrail; Pod-level access logged by `aegis-aws-landing-zone` | Medium — depends on `aegis-aws-landing-zone` controls |

### Information Disclosure

This category is Aegis's highest-stakes threat family. The §9 / ADR-0005
mitigations are designed specifically to minimize it.

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| I1 | Audio PCM leaks via core dump | C++ engine process | ADR-0005 R1: core dumps disabled | Very low |
| I2 | Audio PCM leaks via swap partition | Node OS | ADR-0005 R2: swap disabled on audio nodes | Very low |
| I3 | Audio PCM or transcript leaks into application logs | Log aggregation backend | ADR-0005 R3: compile-time log formatter type whitelist | Very low |
| I4 | Transcript content leaks via OpenTelemetry span attributes | Tracing backend | ADR-0005 R4: span attribute allowlist enforced by custom SpanProcessor | Very low |
| I5 | Transcript or audio leaks via temp file written to host disk | Host filesystem | ADR-0005 R5: tmpfs-only for temp paths | Very low |
| I6 | Backup system (Velero) snapshots audio-processing pod PVs and leaks buffered state | Backup bucket | ADR-0005 R6: audio namespace excluded from Velero; PVCs rejected by admission | Very low |
| I7 | Debug audio dump code in prod build leaks audio | C++ engine binary | ADR-0005 R7: compile-time `#ifdef` stripping; CI grep verification | Very low |
| I8 | Insider (A4) reads process memory of running C++ engine to extract in-flight audio PCM | Operator threat | `ptrace` restricted by Linux `YAMA` kernel hardening; pod `securityContext.capabilities.drop: ALL`; per-pod IAM isolation; least-privilege operator access. Note: voiceprint extraction is structurally impossible because voiceprints are not processed (ADR-0012) | Medium — any operator with shell on a node is a serious threat |
| I9 | Leaked viewer JWT allows A3 to eavesdrop on live prompter content | Viewer transport | Token is session-lifetime only; no historical content exists to replay; rolling 5-line window limits exposure duration | Medium — per ADR-0001 negative consequence |
| I10 | RAG corpus contents leak to unintended tenants | DynamoDB / vector DB | Per-tenant encryption with unique KMS CMK; strict tenant isolation at Go GW authorization layer | Low |
| I11 | Speaker labels with real names leak identity | UI / storage | §9.2 privacy-by-design: UI rejects real-name input at the component layer | Very low |
| I12 | Meeting participant inadvertently disclosed by transcript content mentioning them by name | Any transcript | Not mitigable at architecture level; content-layer PII is handled by transcript being host-local only (§9.1 L3) | Medium — accepted product risk |
| I13 | Transcript used for model training | Model pipeline | §9.8 hard commitment: no training on user data; codified in SECURITY.md. Voiceprint training is structurally impossible (ADR-0012) | Low (depends on organizational discipline) |
| I14 | Local mode host LAN port exposed to malicious device (A7) on shared Wi-Fi | Go Gateway LAN binding | Session JWT required for viewer join (ADR-0007); Local mode's threat model assumes trusted LAN | Medium — do not use Local mode on untrusted networks |

### Denial of Service

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| D1 | Attacker (A1) floods `CreateMeeting` endpoint | Go Gateway / Cognito | Rate limiting at API Gateway / ingress layer; Cognito request quotas | Low |
| D2 | Malicious host floods its own Go GW instance with audio | Go GW session capacity | Per-session PCM rate limit; ADR-0004 bounded fan-out channel capacity | Low |
| D3 | Viewer (A3) establishes many join streams to exhaust fan-out capacity | Go GW fan-out | Per-session viewer limit; per-tenant concurrent session limit | Medium |
| D4 | Large `getDisplayMedia` tab selection loads giant video track | Host browser | Frontend explicitly discards video track immediately; does not send to GW | Low |
| D5 | Malicious dependency introduces infinite loop / OOM in inference | C++ engine | Inference timeout guard; pod resource limits; liveness probe | Low |
| D6 | Rolling deployment drains pod for longer than expected, occupying scheduler slot | K8s scheduler | `terminationGracePeriodSeconds: 14400` (matching `session_max_lifetime`); PDB `maxUnavailable: 1`; HPA slow scale-down policy (ADR-0006) | Low |

### Elevation of Privilege

| # | Threat | Affected | Mitigation | Residual Risk |
|---|---|---|---|---|
| E1 | Viewer token re-used to call host-only APIs | Go GW authorization | Token claims include `role: viewer`; host APIs check role | Low |
| E2 | Tenant A's user accesses Tenant B's session | Go GW authorization | Session registry binds `session_id` to creating tenant's ID; cross-tenant access rejected | Low |
| E3 | Compromised C++ engine process escalates to host OS | Container runtime | Pod `securityContext.runAsNonRoot: true`, `capabilities.drop: ALL`, `readOnlyRootFilesystem: true` except the tmpfs mounts; seccomp profile | Low |
| E4 | Exploit in `getDisplayMedia` browser API escalates to OS | Browser / WebView | Vendor responsibility; Aegis stays on latest stable Chrome/Edge | Accepted |
| E5 | Malicious RAG corpus file exploits vector DB | Vector DB | Corpus uploaded via signed manifest; schema validation at ingest | Low |
| E6 | Compromised viewer abuses `SendOfficerHint` to inject fake staff-authored hints into the room | Go GW authorization | **Current:** any holder of a valid session token can send hints — E1's role-claim mitigation is not yet wired. **Known Gap:** lands with Cognito JWT role claims in Phase 5 auth sprint. Urgency-HIGH/URGENT hints from a compromised viewer could manipulate the meeting's shared context. | **Medium (accepted for MVP)** |

## Boundary Violations to Prevent

The following patterns are **explicit anti-patterns** in Aegis. Any PR
introducing one should be rejected regardless of its other merits.

- ❌ Persisting audio PCM to any durable store (disk, DB, S3).
- ❌ Writing meeting transcript content to any durable store (DynamoDB,
  S3, logs, traces).
- ❌ Adding a runtime flag / environment variable that enables audio
  dumping.
- ❌ Introducing voiceprint enrollment, cosine matching, or any
  biometric processing in any form. ADR-0012 is Accepted and must not
  be reversed without a new superseding ADR that re-evaluates all the
  compliance, UX, and resource trade-offs.
- ❌ Accepting real-name text input for speaker labels.
- ❌ Feeding meeting transcripts into the RAG corpus automatically.

## Review Cadence

- **Minor architectural changes**: threat model is updated in the same
  PR.
- **Major architectural changes** (new storage, new trust boundary,
  new integration): threat model is reviewed in a dedicated security
  review meeting.
- **Full re-review**: at the end of each phase (1, 2, 3, 4, 5).
- **Pre-launch**: Phase 5 Hardening includes a penetration test and
  external threat model review.

## Open Items

- Quantitative risk scoring (e.g., CVSS) for residual risks marked
  "Medium" — not yet done.
- Data Protection Impact Assessment (DPIA) under GDPR — Phase 5, prior
  to any EU customer launch.
- LINDDUN privacy threat modeling as a complement to STRIDE — Phase 5.
- Privacy Engineering review with external counsel for BIPA, CCPA,
  GDPR — before any regulated-industry customer onboarding.
- **Cloud-mode multi-tenancy isolation in the RAG vector store is not
  yet implemented.** Phase 3b Slice 6 ships `engine seed` writing
  corpus chunks to Qdrant with payload `{text, source_path,
  chunk_index}` — deliberately WITHOUT `user_id` / `tenant_id` fields
  — because there is no Cognito JWT source yet to populate them
  honestly (Phase 2 only has a `StaticJWTProvider` scaffold, not a
  live Cognito User Pool). The design decision is captured in
  [ADR-0022 — Cloud-mode multi-tenancy isolation in vector store](adr/0022-cloud-multi-tenancy-isolation.md)
  at Proposed status: **hard tenant boundary = Qdrant collection per
  tenant**, **soft user boundary = payload filter on `user_id`**.
  Threat model implication: until ADR-0022 is implemented (Phase 4
  alongside Cognito wiring), a hypothetical multi-tenant demo deploy
  would have ALL uploaded corpora visible to every authenticated
  user. Acceptable for the demo horizon (single-tenant or
  pre-Cognito anyway); **blocker for any real multi-tenant
  production deployment**. See ADR-0022 §"Deferred decision +
  demo-horizon posture" for the full reasoning.

## Related

- `ARCHITECTURE.md` §9 Data Governance & Privacy
- `ARCHITECTURE.md` §10 Secure SDLC & Supply Chain
- `ARCHITECTURE.md` §11 Known Limitations
- `SECURITY.md`
- ADR-0001 through ADR-0012
