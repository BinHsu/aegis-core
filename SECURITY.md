# Security Policy

Thank you for helping keep Aegis Core and its users safe.

## Supported Versions

Aegis Core is currently in active pre-release development (Phase 1–4 of
the [ROADMAP](ROADMAP.md)). Until the first tagged release, **only the
`main` branch is supported** for security reporting. Post-release, the
supported versions table below will be maintained.

| Version | Supported |
|---|---|
| `main` (pre-release) | ✅ |
| Pre-`v1.0.0` tags | ❌ |

## Reporting a Vulnerability

**Please do NOT open a public GitHub issue for security vulnerabilities.**

We use GitHub's **Private Vulnerability Reporting**. To report a
vulnerability:

1. Navigate to the "Security" tab of this repository.
2. Click "Report a vulnerability."
3. Fill out the form with as much detail as possible.

If GitHub private reporting is unavailable, email
`security@<project-domain-tbd>` with the subject line
`[SECURITY] Aegis Core vulnerability report`.

### What to Include

- A clear description of the vulnerability.
- Step-by-step reproduction instructions.
- The commit hash or version where you discovered it.
- Your assessment of potential impact.
- (Optional) A suggested fix or mitigation.
- (Optional) Whether you wish to be credited publicly after disclosure.

### What to Expect

- **Acknowledgement**: within 3 business days.
- **Initial triage**: within 7 business days. We will tell you whether
  we consider the report a vulnerability, and if so, our severity
  assessment.
- **Status updates**: at least every 14 days until resolution.
- **Coordinated disclosure**: we follow responsible disclosure. We aim
  to ship fixes within **90 days** of acknowledgement for High and
  Critical severity, and **180 days** for Medium, before any public
  disclosure. We will coordinate timing with you if you wish to publish
  your own writeup.
- **Credit**: with your permission, we credit reporters in release
  notes and (eventually) a project security advisories page.

## Scope

### In Scope

- The Aegis Core application: C++ engine, Go Gateway, frontend web
  client, Tauri shell (when built), Bazel build rules.
- The `proto/aegis/v1/aegis.proto` contracts and generated bindings.
- Deployment manifests in this repository (Helm / Kustomize / raw YAML).
- The MVP threat model documented in `docs/threat-model.md`.

### Out of Scope

- The separate [aegis-aws-landing-zone](https://github.com/BinHsu/aegis-aws-landing-zone)
  infrastructure repository. Report infrastructure / AWS account
  vulnerabilities there.
- Third-party dependencies themselves — please report upstream. If the
  vulnerability affects how Aegis *uses* a dependency, that is in
  scope.
- Social engineering of project maintainers.
- Denial-of-service attacks requiring substantial traffic volume.
- Issues in user-specific deployments that are not reproducible from
  this repository's `main` branch.

## Privacy and Data Handling Commitments

Aegis Core makes the following **hard commitments** about how it
handles user data. These are load-bearing for our privacy posture and
are enforced architecturally. If you find a way to violate any of
these in the product or in the deployed service, that is a security
vulnerability by our definition and we want to hear about it.

### What We Do Not Store

- **Raw audio (PCM)** is never persisted anywhere — not on disk, not
  in a database, not in logs, not in traces, not in backups. Audio
  exists only in the running C++ engine process's RAM and is freed
  when the session ends. See `ARCHITECTURE.md` §9.1 and ADR-0005.
- **Voiceprint embeddings** are never persisted. They are generated
  per-session from an enrollment phrase, live only in the C++ engine
  process's RAM, and vanish when the session ends. Every meeting
  requires fresh enrollment.
- **Meeting transcripts** are never persisted on our servers. They
  flow through a bounded in-memory fan-out channel and are
  immediately discarded after delivery to connected viewers. The
  host device is the only place a full meeting transcript ever
  exists, and the host user is its sole custodian.

### What We Do Not Do With User Data

- **We do not train models on user audio, voiceprints, or
  transcripts.** The inference models (`whisper.cpp`, diarization,
  embeddings, optional LLM) are pretrained and used only for
  inference on your in-flight meeting.
- **We do not sell, share, or otherwise disclose user audio,
  voiceprints, or transcripts to any third party.**
- **We do not retain user audio, voiceprints, or transcripts for
  analytics, debugging, QA, product improvement, or any other
  purpose.** When we need to debug transcription quality, we use
  curated synthetic audio fixtures (the "golden audio" test suite),
  never real user recordings.

### What We Do Store (and How)

- **User accounts and Cognito identity records** — encrypted at
  rest, per-tenant KMS CMK.
- **Consent ledger entries** — append-only, for compliance
  evidence. Contains `user_id`, `session_id`, `timestamp`,
  `consent_version`, `client_metadata`. Does **not** contain any
  voiceprint or audio.
- **Tenant metadata and settings** — account configuration, RAG
  corpus pointers, feature flags.
- **RAG knowledge base content** — user-managed documents and
  vector indexes, encrypted per tenant. This is **not** populated
  from meeting transcripts.
- **Operational metadata** — request IDs, session IDs, duration
  metrics, error codes. No transcript or audio content.

### Regulatory Posture

- **GDPR**: voiceprints are Art. 9 special-category data; handled
  with explicit consent, data minimization, and structural storage
  limitation (RAM-only, session-scoped).
- **BIPA (Illinois)**: voiceprints qualify as biometric identifiers;
  handled with written consent capture and non-retention.
- **CCPA**: voice recordings qualify as sensitive personal
  information; handled via the same mechanism.
- **Subject Access Requests**: users may request the content Aegis
  holds about them. In the MVP, our honest answer for meeting
  content is: *"We do not hold your meeting audio, voiceprints, or
  transcripts on our servers. That content lives only on your
  device."* We do provide the account and consent ledger records
  we hold.
- **Right to Erasure**: see `ARCHITECTURE.md` §9.5.

## Security Controls Summary

This is a non-exhaustive summary; the authoritative reference is
`ARCHITECTURE.md` §8 Enterprise Standards and §10 Secure SDLC.

- **Zero Trust networking** with mTLS between Go Gateway and C++
  Engine.
- **EKS Pod Identity** for AWS resource access; no static
  credentials.
- **Cognito JWT** for end-user authentication; short-lived invite
  tokens for viewer access.
- **Signed container images** (Cosign / Sigstore) verified at
  deployment admission.
- **SBOM** (CycloneDX) published with every release.
- **SLSA Level 3** provenance for release pipelines.
- **Secret scanning** (gitleaks + GitHub push protection).
- **SAST / DAST** (CodeQL, Semgrep, Trivy).
- **Core dump disabled**, **swap disabled**, **tmpfs-only temp**,
  and other ADR-0005 requirements on audio-handling nodes.
- **Private vulnerability disclosure** (you are here).

## Threat Model

See [docs/threat-model.md](docs/threat-model.md) for the full STRIDE
threat model with attacker profiles, identified threats, mitigations,
and residual risk assessments.

## Bug Bounty

There is no paid bug bounty at this stage. The project is
pre-release, self-funded, and open source. We will reassess as the
project matures and post-release commercial arrangements take shape.

We do recognize security researchers publicly (with permission) and
cite contributions in release notes.

## Hall of Fame

*This section will be populated once we have received and resolved
our first external security report.*

## Questions

If you have general security questions (not a vulnerability report),
open a **Discussion** in this repository under the "Security"
category.

For questions about compliance posture (GDPR, BIPA, SOC 2 timeline),
please contact the project owner directly via the repository's
contact information.
