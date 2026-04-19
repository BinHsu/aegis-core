# Interview Notes — Aegis Core

<!-- session-close-review: recruiter-facing narrative -->

> **Who this document is for.** Recruiters, hiring managers, and HR
> partners evaluating me for a role. It is deliberately written without
> jargon; if you can read a business email, you can read this. A
> separate technical walkthrough lives in `README.md` and
> `docs/adr/` — hand those to your engineering team when you want
> depth, and they can verify every claim below in the code itself.
>
> **Reading time.** 7 minutes.

---

## The 30-second version

I am a **hands-on architect** — I make the cross-cutting decisions on
a software project (which languages, which protocols, how the pieces
fit together, what's in scope, what isn't) *and* I write the code that
enforces them. My specialty is not any single layer of the stack; it
is the **seams between layers** — build-system design, type contracts
across language boundaries, protocol definitions, scope artifacts that
survive team handover.

This repo (`aegis-core`) is evidence. Every architectural decision
below has my commit next to it; every ADR was written before the code
it governs; every type-safety guarantee is compile-checked, not
lint-checked.

My style: **pick proven open-source building blocks, compose them
correctly (including the classes of composition bug that proven
libraries don't protect you from), write down every decision so the
next person on the team can audit, challenge, or replace it.**

If your team needs someone who can own "the shape of the whole system"
AND show up to pair-program the tricky parts — that's the role I do
best.

---

## What this project is, in plain language

**Aegis Core** is a live-transcription tool for meetings. Think: a
meeting starts, the host's browser picks up the audio, a speech-to-text
model turns it into text on the fly, and invited viewers see the
transcript streaming in real time on their own devices.

The novel constraint is **privacy**: in "Local mode" (a company's own
laptop / server), the audio never leaves the host's machine — the
transcription happens locally, and only the text is sent to viewers. In
"Cloud mode" (for teams who prefer centralized operation), the same
software runs on a cloud server with the same guarantees about what
gets stored where.

**What's working today** (clone the repo and see for yourself):

1. One command brings up the whole system: `bazel run //:app_local`.
2. A real microphone recording goes in; a real transcript comes out.
3. The automated test suite proves it end-to-end against a reference
   audio clip.
4. A text corpus goes in via `engine seed`; the retrieved embeddings
   are within a validated margin of the floating-point reference, so
   retrieval quality is quantified, not asserted.

---

## What this repo demonstrates about me as a hire

Each bullet below is backed by something concrete in the repo that your
technical team can verify.

- **I make decisions and write them down.** The `docs/adr/` folder
  contains the architecture decision record log, growing as the
  project evolves. Each ADR names the problem, the options considered,
  the choice made, and the reasoning. When I change my mind, I write
  a new ADR — I don't quietly rewrite history.

- **I ship end-to-end, not component-by-component.** The project
  spans three programming languages (C++ for the speech-recognition
  core, Go for the server that glues everything together, TypeScript
  for the web frontend). All three build under one tool chain, all
  three share one set of protocol definitions, and a single command
  starts the whole thing.

- **I am honest about what I deliberately did not build.** The
  `README.md` and `ROADMAP.md` both contain a "Known Gaps" section
  that names exactly what is missing, why it was skipped, and what a
  future contributor would need to do to close each gap. This
  discipline is usually more telling than a feature list — it is the
  signal that I know how to say "this is out of scope for now", which
  is rarer than "I built everything I could think of".

- **I choose boring, well-maintained dependencies.** I did not
  reinvent speech recognition, I integrated `whisper.cpp`. I did not
  write a WebRTC implementation, I used `pion/webrtc`. I did not
  design a new token format, I used industry-standard JWT. My
  contribution is **correctly assembling trusted pieces** and verifying
  the assembly in tests.

- **I build for the next engineer's benefit.** Every non-obvious
  design choice has an inline comment explaining *why* it was made,
  not just what it does. The goal is that someone new to the
  codebase can work productively within a day — not a week.

- **I catch the cross-cutting bug classes an architect should catch
  — and I write the test that proves it.** One concrete example: when
  the code validates authentication tokens, there's a specific test
  that confirms the classic `alg=none` downgrade attack is rejected
  (this is a JWT implementation pitfall that has historically
  compromised production systems). I know the class of bug to watch
  for and encode that knowledge in tests — the tests are in the repo,
  under my own authorship, not delegated to "whoever picks this up".

- **I follow rules I've written for myself.** The repo contains
  `CLAUDE.md`, a charter of engineering rules — about testing, about
  documentation, about incident postmortems — that I hold myself to. I
  can point at my own commits and show the charter being followed,
  including a postmortem log of real dev-time problems I hit and fixed
  along the way (`docs/incidents.md`).

---

## Governance posture — GitOps, DevSecOps, FinOps (verifiable row-by-row)

Three frameworks your platform / security / finance stakeholders will
ask about. Each table below maps the principle to the **specific
file or ADR** that realises it. No hand-waving — click through and
verify.

### GitOps — repo is the source of truth; every change is a reviewable diff

| Principle                                | How this repo realises it                                                                                         | Artifact                                                                                              |
| ---------------------------------------- | ----------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Declarative system state                 | Every build, dep, and codegen step declared in Bazel; no host-installed tooling allowed to influence the outcome. | `MODULE.bazel`, `CLAUDE.md` Rule 6, `ADR-0008`, `ADR-0015`                                            |
| Architecture decisions as code-reviewed artefacts | ADRs are PR'd and committed before the code they govern; deprecated choices get superseding ADRs, not quiet rewrites. | `docs/adr/0001` … `docs/adr/0024`                                                                     |
| Roadmap + Known Gaps in the tree, not in a wiki | Release horizon + deliberate-omission list live in git so they diff with the code they describe.                  | `ROADMAP.md`, `README.md` §"Known Gaps"                                                               |
| Conventional Commits, signed, `--force-scope` | Pre-commit hook rejects non-Conventional, non-signed, or scopeless messages — no ad-hoc history.                  | `.pre-commit-config.yaml` (conventional-pre-commit `--force-scope`), `docs/github-setup.md` §0.5      |
| PR-gated CI as merge criterion           | 8-job matrix (lint, codegen drift, secrets, proto, Bazel tests, Tauri-compliance, Playwright, link-check) blocks merge. | `.github/workflows/ci-baseline.yml`                                                                    |
| Reproducible-clone promise               | `bazelisk` wrapper + `.bazelrc` `--output_user_root=./.bazel_cache` + hermetic Node + repo-local Playwright browsers. | `tools/bazelisk/bazelisk`, `.bazelrc`, `tools/scripts/frontend.sh`, `ADR-0015`                         |
| Cross-repo coordination is also a diff   | Platform dependencies requested of the landing-zone repo via labelled GitHub issues, not Slack DMs.               | `gh issue #11` (cross-repo standing), `docs/github-setup.md`                                          |

### DevSecOps — security is a pipeline stage, not a late-stage audit

| Principle                                 | How this repo realises it                                                                                          | Artifact                                                                                             |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Secrets never land in commits             | Gitleaks runs at pre-commit **and** on every PR; `.gitignore` blocks `.env`, `*.pem`, `*.key`, `credentials.json`. | `.pre-commit-config.yaml` (gitleaks hook), `.github/workflows/ci-baseline.yml` (secret-scan job), `.gitignore` |
| Known-pitfall auth tests                  | JWT `alg=none` downgrade is a specific unit test, not a review checkbox.                                           | `gateway_go/internal/token/jwt_test.go`, `gateway_go/internal/auth/auth_test.go`                     |
| Privacy by design at the UI layer         | Speaker labels = closed curated list (no free-text) per GDPR Art. 25; enforced by reducer + compliance grep.       | `frontend_web/src/pages/Host/HostPage.tsx` (`CURATED_SPEAKER_LABELS`), `ARCHITECTURE.md` §9.2        |
| Consent is auditable, not implicit        | Two-phase consent (audio + transcript-export) with stable record shape for the Phase-4 ledger drop-in.             | `ADR-0024`, `frontend_web/src/lib/consent.ts`, `frontend_web/src/components/*Consent*.tsx`           |
| Ephemeral audio is a code invariant       | Seven binding requirements on the C++ engine; no "enable recording" toggle exists.                                 | `ADR-0005`, `ADR-0012`, `ARCHITECTURE.md` §9.1                                                       |
| Cross-WebView compatibility gate          | ADR-0002 Constraints 1–6 enforced by a grep script; CI fails the PR on `chrome.*`, Service Worker, `SharedArrayBuffer`, `user-select: none` on transcript, etc. | `tools/scripts/check_frontend_tauri_compliance.sh`, `ADR-0002`                                       |
| Live-browser regression guard             | Playwright chromium + webkit smoke tests the consent flow in real engines (Incident-09 lesson in code).            | `frontend_web/e2e/consent-smoke.spec.ts`, `docs/incidents.md` #09                                    |
| Container build is hermetic + runtime-gated | OCI image built via Bazel `rules_oci` (no Dockerfile, no daemon); distroless `static-debian12:nonroot` pinned by SHA256 digest; mandatory CI step boots the image with `--read-only --user 65532:65532` and curls `/healthz` (200 within 1s in practice) — Camp B / dev-CI split makes this the leftmost gate of the promotion chain.  | `ADR-0025`, `packaging/gateway/BUILD.bazel`, `.github/workflows/ci-baseline.yml` (`Smoke-test gateway OCI image` step) |
| SBOM emitted per image, machine-readable | Every container image gets a CycloneDX SBOM (Syft via `anchore/sbom-action`, SHA-pinned for reproducibility); uploaded as workflow artifact, slated to become a signed Cosign attestation in Phase 4b. ARCHITECTURE.md §10.1 promise is half-live (gateway done; engine + frontend land with Slices 4 / 5). | `ADR-0025` §"Sequencing across slices", `.github/workflows/ci-baseline.yml` (`Generate gateway image SBOM` step), `ARCHITECTURE.md` §10.1 |
| Push-to-ECR via OIDC, least-privilege isolated | Dedicated `release-staging-image.yml` workflow on `push: branches: [main]` only — never on PRs (matches the IAM role's trust scope; `job_workflow_ref` IAM condition further pins to this file). Separate from `ci-baseline.yml` so the `id-token: write` blast radius is one file. SHA-pinned `aws-actions/configure-aws-credentials` + `aws-actions/amazon-ecr-login`. Tag scheme `staging-<git_sha>` (gateway) / `engine-staging-<git_sha>` (engine). | `ADR-0025` Promotion-chain Gate 3, `.github/workflows/release-staging-image.yml`, ldz issue #79 (cross-repo posture confirmation) |
| Engine packaging trades carefully across cost, complexity, blast-radius | C++ engine image (Slice 4a-4) gated to Linux exec via `target_compatible_with` (Mac gets loud incompatibility error, never silent broken binary); models NOT baked in image (mount PV at runtime — image ~50-100MB vs 1.5GB+ if baked, and decouples model upgrades from artifact rotation); distroless `static-debian12:nonroot` tried first with one-line fork-point comment to swap to `base-debian12` if linker drift surfaces. Each decision documented with the alternative's cost in ADR-0025 §"Slice 4 distroless variant decision". | `packaging/engine/BUILD.bazel`, `ADR-0025` §"Slice 4 distroless variant decision" |
| Frontend deploys via CloudFront (not a container — caught a category error in original ROADMAP) | Phase 4a-5 ships React+Vite SPA via `aws s3 sync` + `cloudfront create-invalidation` on every main push. Split subdomains (`aegis-app.staging.binhsu.org` for SPA, `aegis-api.staging.binhsu.org` for gateway) — path-based routing at CDN was rejected because CloudFront's `/ws/*` WebSocket-upgrade story is awkward. CORS allowlist (`AEGIS_ALLOWED_ORIGINS` env var, new `gateway_go/internal/cors/` package with table-driven test) closes a Phase 2 Known Gap. Cross-repo coord with ldz (#90 → #91) confirmed Option C was their long-standing plan; ldz provisions in ~3hr when our PR drafts. | `ADR-0027`, `.github/workflows/release-staging-frontend.yml`, `gateway_go/internal/cors/cors.go`, ldz #90 / #91 |
| Incident discipline                       | Dev-time postmortems with root cause + failed-attempt narrative; linked commit hashes; no marketing gloss.         | `docs/incidents.md`, `CLAUDE.md` Rule 7                                                              |

### FinOps — spend awareness is an architectural decision, not a later optimisation

| Principle                                | How this repo realises it                                                                                          | Artifact                                                                                             |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------- |
| Demo horizon = near-zero idle cost       | In-memory session state (refresh = meeting ends); no always-on cache/DB for the demo tier.                         | `ADR-0023` Decision A, `gateway_go/internal/grpc/gateway_service.go`                                 |
| Pay-per-query stateful tier              | DynamoDB On-Demand for the consent ledger when it lands; idle ≈ 0, billed only on real events.                     | `ADR-0022` (multi-tenancy + pricing choice), cross-repo dependency on `aegis-aws-landing-zone`       |
| Remote-cache SaaS tier matches usage     | BuildBuddy Personal free tier for the demo; plan documented for S3+OIDC at production volume.                      | `ADR-0014` (Option β → Option δ), `docs/runbooks/buildbuddy-cache-setup.md`                          |
| RAG retrieval is opt-in                  | Empty `rag_id` is first-class "no corpus" mode; no corpus → no embedding calls → no vector-DB query cost.          | `ADR-0023` Decision B, `proto/aegis/v1/aegis.proto` (`CreateMeetingRequest.rag_id`)                   |
| Self-hosted vector store for the MVP     | Qdrant binary + storage dir live inside the repo; no managed SaaS billing until usage justifies it.                | `ADR-0019`, `docs/runbooks/qdrant-local-setup.md`                                                    |
| Models pulled on demand, not baked in    | `models/` gitignored; SHA-verified downloads per build; no fat images shipping 75 MB of weights.                   | `.gitignore` §AI model artifacts, `ARCHITECTURE.md` §10.1                                            |
| Skipped features = avoided spend         | Explicit no-biometric, no-voiceprint, no-durable-transcript decisions. The cheapest line item is the one you don't build. | `ADR-0012`, `ARCHITECTURE.md` §9.1, `ROADMAP.md` §"Known Gaps"                                   |
| Engine owns inference (no Python runtime)| bge-m3 + Whisper run via llama.cpp C API inside the engine binary; no SageMaker endpoint on standby, no per-token API bill. | `ADR-0020`, `ADR-0021`, `engine_cpp/src/inference/ggml_embedder.cc`                                 |
| Cloud infra cost evidence is in its own repo | Separation keeps Terraform / billing alerts / CUR dashboards out of the app repo's review surface.             | [`aegis-aws-landing-zone`](https://github.com/BinHsu/aegis-aws-landing-zone) (linked throughout)     |

These three tables are the short answer when a platform / security /
finance reviewer asks "does this repo match our governance posture?".
Each row is **falsifiable**: if the linked artefact doesn't back the
claim, the claim is wrong and the table is the bug.

---

## What I am *not* claiming

I am being specific here because a mismatched hire is expensive for
everyone. If the role is centered on any of the following, I am
probably not the strongest candidate:

- **Deep machine-learning research.** I integrate a pre-trained
  speech-recognition model. I do not train, fine-tune, or redesign
  models. If the role is "improve our speech-recognition accuracy by
  2 percentage points", that's a specialist's job.

- **Real-time audio signal processing.** I pick libraries that handle
  audio conversion correctly. I do not write echo cancellation
  algorithms or jitter buffers from scratch.

- **Cryptographic primitive design.** I use established libraries and
  know the common ways to *misuse* them (hence the `alg=none` test).
  I do not invent new encryption schemes.

- **Deep Kubernetes / cloud-operations work.** There *is* an ADR in
  this repo explaining how the system is meant to be deployed, but the
  actual cloud-infrastructure evidence lives in the companion
  [`aegis-aws-landing-zone`](https://github.com/BinHsu/aegis-aws-landing-zone)
  repo. That repo gets its own walkthrough on request.

---

## What kind of role fits

**Target level.** Staff / Principal engineer; hands-on architect; tech
lead with real commit activity. Someone responsible for "the shape of
the thing" AND the tricky parts of the code that enforce it — not a
pure-strategy architect who only produces slides.

**Target function.** Backend and platform architecture. Designing
protocols, choosing build systems, enforcing type contracts across
language boundaries, setting team conventions, mentoring, writing
ADRs, running technical reviews — *and* implementing the hard seams
myself (build-graph surgery, cross-runtime type guarantees, protocol
multiplexing, graceful-degradation paths).

**Where the two repos land.**

- `aegis-core` (this repo) — **backend + platform** architecture. How
  services are designed, how the code is structured, how the build
  works, how contracts between languages are maintained.
- [`aegis-aws-landing-zone`](https://github.com/BinHsu/aegis-aws-landing-zone)
  (separate repo) — **cloud infrastructure**
  architecture. How the above runs on AWS under strict compliance,
  deployment pipelines, cost visibility, incident response.

Ask me about one or the other depending on what you are screening for.

---

## For your technical team

If your engineers want to verify any of the above, point them at these
entry points in order — the whole walkthrough takes about 15 minutes:

1. **`README.md`** → start here. "Quick Start", "Known Gaps", ADR
   index table.
2. **`docs/adr/`** → the 14 architecture decisions. Each is under 300
   lines and explicitly names its alternatives.
3. **`ROADMAP.md` → Phase 2 Known Gaps** → what was deliberately not
   built, and why. This is the "judgment" evidence.
4. **`docs/incidents.md`** → real dev-time postmortems, including
   failed fix attempts and red herrings. This is the "intellectual
   honesty" evidence.
5. Any specific decision flagged in the ADR index — click through to
   read the reasoning, then look at the corresponding code path
   referenced by the ADR.

Your team should feel free to challenge any decision. I won't be
offended — that's literally what ADRs exist for.

---

## Contact

Reach me via the GitHub profile linked from the repo's top-level
`README.md`. If anything above matches a role you're screening for, I
can prepare a 20-minute walk-through against a real running instance of
the system. Most candidates will hand you a deck; I'll hand you a
terminal.

---

*This document is maintained alongside the code. If anything here
stops matching reality, the code wins and this file gets a correction
pull request.*
