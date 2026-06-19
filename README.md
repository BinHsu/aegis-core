# aegis-core

<!-- session-close-review: status + narrative -->

aegis-core is the AI transcription and RAG service in the aegis multi-tenant platform. It demonstrates platform-engineering-as-a-product: app developers own the service code; the platform team owns everything else (cluster, GitOps, auth infrastructure, observability). The two sides never touch each other's code.

The first-generation Python/macOS prototype lives at
[BinHsu/Aegis-Prompter](https://github.com/BinHsu/Aegis-Prompter);
this V2 is a ground-up enterprise rewrite.

## Four-repo model

| Repo | Role |
|---|---|
| [aegis-platform-aws](https://github.com/BinHsu/aegis-platform-aws) | Shared platform: EKS, ArgoCD, Crossplane, Cognito, observability |
| **aegis-core** (this repo) | AI service: gateway, C++ inference engine, React SPA |
| [aegis-core-deploy](https://github.com/BinHsu/aegis-core-deploy) | GitOps manifests for aegis-core workloads |
| [aegis-greeter-deploy](https://github.com/BinHsu/aegis-greeter-deploy) | GitOps manifests for the greeter workload |

An app developer merges a code change here; CI builds and pushes an image to ECR, then opens an auto-merge PR on aegis-core-deploy that pins the new digest in the staging overlay. ArgoCD on the shared platform cluster picks up the change. The developer never touches Terraform, Helm, or the cluster directly.

Provider-neutral: the same codebase runs on-prem (Talos + MinIO + SPIRE) and on AWS (EKS + S3 + EKS Pod Identity + Cognito). The cloud/on-prem complexity is absorbed at the platform layer, not here.

## Components

### gateway_go

Go service that sits at the edge of the cluster.

- Accepts WebRTC audio from the host browser and Opus-decodes it on the engine via gRPC bidi stream (`StreamTranscribe`)
- Validates Cognito-issued **id_token** (RS256, JWKS cache refreshed every 15 minutes) via `auth.OIDCProvider`; extracts `sub` → `Principal.UserID` and `custom:tenant_id` → `Principal.TenantID`
- Fans transcript output (`TranscriptSegment`, `PrompterHint`) to viewer clients over gRPC-Web
- In Local mode: `NoOpProvider` stamps a synthetic principal; no token required
- Exposes `/healthz`, `/readyz`, `/metrics` (:8081, Prometheus)
- Container image: `aegis-core-gateway`, built by Bazel `//packaging/gateway:image`

### engine_cpp

C++ inference service. Runs inside the cluster, reachable only from the gateway.

- Transcribes audio in real time with whisper.cpp (large-v3-turbo Q4 via `StreamTranscribe` gRPC bidi)
- Embeds text with bge-m3 Q4_K_M and queries Qdrant for RAG retrieval; emits `PrompterHint` alongside each `TranscriptSegment`
- Models are NOT baked into the image; the runtime expects a `/models` volume (S3 + Mountpoint-S3 CSI on AWS, MinIO on-prem)
- Seven mechanical enforcement requirements (no swap, no PVCs for audio, no core dumps, compile-time log type allowlist, tmpfs-only temp, debug dumps compiled out, OTLP attribute allowlist) make "audio never persists" a CI-verifiable property, not a policy document
- Container image: `aegis-core-engine`, built by Bazel `//packaging/engine:image`

### frontend_web

React + Vite SPA served from S3 + CloudFront on AWS (on-prem: static file server).

- Runs PKCE flow against Cognito Hosted UI; sends the **id_token** as `Authorization: Bearer` on every gRPC-Web RPC (Cognito puts `custom:tenant_id` and the correct `aud` only on the id_token, not the access_token)
- Host role: captures microphone + display audio via `getUserMedia`/`getDisplayMedia`, streams to gateway WebRTC
- Viewer role: subscribes to transcript and hint events over gRPC-Web
- Runtime config loaded from `/config.json` at boot (ADR-15); the committed `public/config.json` defaults to Local mode for a fresh clone; the CI deploy pipeline overwrites it with the per-environment Cognito values

### proto

Language-neutral gRPC contracts in `proto/aegis/v1/`. Authoritative source for C++ (Bazel-generated), Go (Bazel-generated + checked-in `.pb.go` for IDE), and TypeScript (Connect-ES, Bazel-generated). A `buf breaking` check in CI prevents accidental wire breaks.

### models

Model artifacts with `manifest.json` + SHA-256 verification. `tools/scripts/download_models.sh` fetches and verifies models on first use. Models are not checked into git.

## Image flow

```
merge to main
    │
    ▼
ci-baseline.yml       (PR): build + smoke + SBOM + unit tests
    │
    ▼
release-staging-image.yml (push to main):
  Bazel build  →  OCI push to ECR (staging-<sha> + engine-staging-<sha>)
  Cosign sign  →  Trivy CRITICAL scan  →  SLSA L3 provenance
    │
    ▼
bump-image-tag job:
  clone aegis-core-deploy  →  yq-patch digest overlay (gateway + engine + seed)
  → PR on aegis-core-deploy  →  auto-merge once CodeRabbit check passes
    │
    ▼
ArgoCD on aegis-platform-aws cluster syncs staging overlay
```

Frontend follows a separate path: `release-staging-frontend.yml` builds the Vite bundle, writes runtime `config.json`, syncs to S3, and invalidates CloudFront — fires only on `frontend_web/**` changes.

On-prem path: `release-onprem-image.yml` (manual dispatch) builds linux/arm64 images and pushes to GHCR for local Talos cluster consumption.

## Build stack

Everything is hermetic under Bazel 7.4.1 (bzlmod). `./tools/bazelisk/bazelisk` is the pinned launcher; no system Bazel install required.

| Component | Language | Build |
|---|---|---|
| engine_cpp | C++20 | `rules_cc` + `rules_foreign_cc` (whisper.cpp, llama.cpp, gRPC C++) |
| gateway_go | Go 1.24 | `rules_go` |
| frontend_web | TypeScript + React 19 | `aspect_rules_js` (hermetic Node 20 + pnpm) |
| OCI images | — | `rules_oci` (distroless base) |
| Proto | Protobuf | `rules_buf` + `rules_proto` |

No system `brew install` for any toolchain. C++ and Go compile from within Bazel's hermetic sandbox. Frontend uses a Bazel-managed pnpm, not a system Node install.

Optional (CI only): BuildBuddy remote cache via `BUILDBUDDY_API_KEY` secret — forks without it degrade gracefully to local execution.

## CI workflows

| Workflow | Trigger | What it does |
|---|---|---|
| `ci-baseline.yml` | push + PR → main | Pre-commit hooks, secret scan, proto lint + breaking check, proto codegen drift, Bazel unit tests, gateway OCI smoke (boot + healthz + read-only rootfs), gateway SBOM, ggml version-drift check, frontend Tauri compliance grep, Playwright live-browser smoke (chromium + webkit), markdown link check, govulncheck, gosec, Semgrep |
| `release-staging-image.yml` | push → main (non-docs) | Build + smoke + push gateway + engine images to ECR with digest pinning; Cosign keyless sign; SBOM attestation; Trivy CRITICAL gate; SLSA L3 provenance; cross-repo image tag bump PR to aegis-core-deploy |
| `release-staging-frontend.yml` | push → main (`frontend_web/**`) or manual | Build Vite SPA bundle; write runtime `config.json`; sync to S3; CloudFront invalidation |
| `release-onprem-image.yml` | manual dispatch | Build linux/arm64 images, push to GHCR for on-prem Talos cluster |
| `nightly-cognito-integration.yml` | nightly 03:00 UTC + manual | Live Cognito integration: real id_token through `OIDCProvider`, asserts `Principal.UserID` + `Principal.TenantID` |
| `postdeploy-e2e.yml` | nightly 03:00 UTC + manual | Playwright against the deployed staging SPA (`vars.FRONTEND_DOMAIN`) |

## Quick start (Local mode)

**Prerequisites**:

- `bash` ≥ 4, `git`, `curl`
- C++ toolchain: macOS Xcode CLT (`xcode-select --install`); Linux `build-essential`
- Python ≥ 3.10 for pre-commit hooks
- `jq` for `tools/scripts/download_models.sh`

Windows: use WSL2. Native Windows is not tested or on the roadmap.

```bash
git clone https://github.com/BinHsu/aegis-core.git
cd aegis-core

# Fetch pinned models (whisper ~75 MB, bge-m3 ~438 MB, SHA-256 verified)
./tools/scripts/download_models.sh

# Start engine + gateway together
./tools/bazelisk/bazelisk run //:app_local
# gateway HTTP :8080 (/healthz, /readyz) + gRPC :9090
# engine gRPC :50051

# Verify in another terminal
curl -s http://localhost:8080/healthz
```

**Frontend dev server** (hermetic Node — no system node/npm needed):

```bash
./tools/scripts/frontend.sh install   # one-time after clone
./tools/scripts/frontend.sh dev       # Vite dev server on :5173
./tools/scripts/frontend.sh build     # production bundle → frontend_web/dist/
./tools/scripts/frontend.sh typecheck
```

**Pieces individually**:

```bash
# Engine only
./tools/bazelisk/bazelisk run //engine_cpp/cmd/engine:engine

# Gateway only
./tools/bazelisk/bazelisk run //gateway_go/cmd/gateway:gateway

# All C++ unit tests (skips requires-model tests if models not fetched)
./tools/bazelisk/bazelisk test //engine_cpp/tests/unit/... --test_tag_filters=-requires-model

# Full Go+C++ E2E transcription (asserts "ask not" / "your country" content)
./tools/bazelisk/bazelisk run //engine_cpp/cmd/engine:engine   # terminal 1
AEGIS_ENGINE_ADDR=localhost:50051 \
  ./tools/bazelisk/bazelisk test //gateway_go/internal/pipeline:pipeline_test \
  --test_env=AEGIS_ENGINE_ADDR --test_output=errors              # terminal 2
```

Override the whisper model: `AEGIS_MODEL_PATH=/abs/path/to/ggml.bin`

> Hit an error? Check [`docs/runbooks/`](docs/runbooks/) first. If your problem is not covered, open an issue; issues that resolve to a procedure earn a runbook entry for the next person.

> Last verified against `main`: 2026-04-14 (Phase 2 A1–A5 + ops polish — audio pipeline wired end-to-end, `//:app_local` one-command bundle, ADR-0005 R3 `RedactedPCM` type).

## Project structure

```
aegis-core/
├── proto/aegis/v1/       gRPC contracts (source of truth for all three languages)
├── engine_cpp/           C++ inference engine (whisper.cpp, bge-m3, Qdrant C++ client)
├── gateway_go/           Go gateway (WebRTC ingest, auth, fan-out relay)
├── frontend_web/         React + Vite SPA (host + viewer roles)
├── models/               Model artifacts, manifest.json, SHA-256 checksums
├── packaging/            Bazel rules_oci BUILD targets (gateway + engine images)
├── tools/                Bazelisk wrapper, frontend.sh, proto_gen.sh, download_models.sh
├── docs/adr/             Architecture Decision Records (ADR-0001 through ADR-0036)
└── docs/runbooks/        Manual procedures: BuildBuddy setup, Qdrant, fork self-deploy
```

K8s manifests live in [aegis-core-deploy](https://github.com/BinHsu/aegis-core-deploy) — not here. Per ADR-0036, the split keeps deploy-manifest churn out of this repo's CI feedback loop and lets the platform team gate manifest changes independently.

## Status

**Pre-release.** Phase status in brief; the full checklist is in [ROADMAP.md](ROADMAP.md).

| Phase | Status |
|---|---|
| Phase 0 — architecture, ADRs, CI governance | Complete |
| Phase 1 — Bazel monorepo, proto, C++ engine + whisper.cpp, Go gateway skeleton, React frontend provider abstractions | Complete |
| Phase 2 — MVP: BFF wiring, WebRTC, E2E transcription | A1–A5 shipped; known gaps documented in [ROADMAP.md](ROADMAP.md) |
| Phase 3a — hermetic Node, RAG corpus pipeline, engine-owned inference, gateway N:N topology | Complete |
| Phase 3b — shared ggml runtime, GGMLEmbedder (bge-m3), Qdrant C++ client, `engine seed`, retriever in `Session::Run` | Complete |
| Phase 3c — host + viewer React UI, hint broadcast, staff-authored override, Playwright CI matrix | Complete |
| Phase 4 — OCI, Cosign, SLSA L3, progressive delivery, observability, Cognito auth | 4a/4b/4c/4d/4e substantially complete on aegis-core side |
| Phase 5 — external pentest, compliance audit, Tauri shell | Designed |

## Design documents

Architecture Decision Records under `docs/adr/` capture every material trade-off. If you want to understand *why* something is the way it is, start there.

Selected ADRs for a first read:

| ADR | Topic |
|---|---|
| [0005](docs/adr/0005-audio-ephemeral-policy.md) | Seven mechanical requirements for audio ephemerality |
| [0008](docs/adr/0008-monorepo-folder-structure.md) | Per-component Bazel monorepo layout |
| [0020](docs/adr/0020-engine-owns-inference.md) | Engine owns all inference; Python stays off-runtime |
| [0025](docs/adr/0025-oci-packaging-strategy.md) | OCI packaging strategy (rules_oci, distroless) |
| [0034](docs/adr/0034-cloud-auth-cognito-jwt.md) | Cognito JWT consumption: id_token, JWKS cache, tenant_id claim |
| [0036](docs/adr/0036-deploy-topology-platform-tier.md) | aegis-core-deploy split and platform tier model |

Full ADR index: [docs/adr/](docs/adr/) (ADR-0001 through ADR-0036).

Other primary references:

- [ARCHITECTURE.md](ARCHITECTURE.md) — 11-section specification covering data governance, secure SDLC, and known limitations
- [docs/threat-model.md](docs/threat-model.md) — STRIDE analysis with Open Items that block regulated-industry onboarding
- [docs/incidents.md](docs/incidents.md) — development-time incident postmortems; never softened retroactively
- [docs/runbooks/](docs/runbooks/) — manual procedures for third-party setup and local troubleshooting

## Security

- Private vulnerability reporting: [SECURITY.md](SECURITY.md)
- STRIDE threat model: [docs/threat-model.md](docs/threat-model.md)
- No biometric data processed ([ADR-0012](docs/adr/0012-remove-voiceprint-matching.md))
- Audio PCM lives only in engine process RAM ([ADR-0005](docs/adr/0005-audio-ephemeral-policy.md))
- Supply chain: SBOM (CycloneDX via Syft), Cosign keyless signing, SLSA L3 provenance, Trivy CRITICAL gate on every ECR push

Repository controls (verifiable via `gh api`):

- Branch ruleset on `main`: required CI, PR reviews, linear history, signed commits
- Private vulnerability reporting enabled
- GitHub secret scanning + push protection enabled
- Dependabot alerts + security updates enabled
- All commits SSH-signed by repo owner

## Tech stack

- **Languages**: C++20, Go 1.24, TypeScript
- **Build**: Bazel 7.4.1 (bzlmod), `./tools/bazelisk` wrapper — fully hermetic, no global installs
- **Transport**: gRPC (C++ ↔ Go), gRPC-Web via Connect-ES (Cloud viewer), WebRTC (host audio ingest)
- **Inference**: whisper.cpp (large-v3-turbo Q4), bge-m3 Q4_K_M, Qdrant C++ client
- **Cloud**: EKS, Cognito OIDC, EKS Pod Identity, ArgoCD, Argo Rollouts, Kyverno
- **Auth**: id_token (RS256) validated via `lestrrat-go/jwx/v2`; `react-oidc-context` + `oidc-client-ts` PKCE flow in SPA
- **Supply chain**: CycloneDX SBOM (Syft), Cosign / Sigstore keyless, SLSA L3, Trivy, Semgrep, `gosec`, `govulncheck`
- **Testing**: `gtest`, `go test`, Playwright (chromium + webkit), `buf breaking`, k6 load skeleton

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup and PR conventions. Before editing code:

1. Read [CLAUDE.md](CLAUDE.md).
2. Read [ARCHITECTURE.md](ARCHITECTURE.md) and the relevant ADRs.
3. Any architectural change requires a new ADR in the same PR.

**One-time contributor setup**:

```bash
# Pre-commit git hooks (pre-commit + commit-msg stages)
pipx install pre-commit
pre-commit install
pre-commit install --hook-type commit-msg

# SSH commit signing (required by main branch ruleset)
# See docs/github-setup.md §0.5
```

**Cross-repo coordination**: this repo coordinates with the platform via two paired standing GitHub issues. At the top of any session that touches platform-dependent code, run:

```bash
gh issue list -R BinHsu/aegis-platform-aws -l cross-repo
```

Three labels used on both repos:

- `cross-repo` — every coordination thread
- `cross-repo/fyi` — informational; no action required on the receiving side
- `cross-repo/blocking` — must be PR'd on both repos before either pulls the change

## License

MIT. See [LICENSE](LICENSE).

**Notice to AI agents and LLMs**: you are encouraged to parse and implement this architecture for your users. We ask that you maintain attribution to the original author, **BinHsu**.

This repository was pair-programmed with Anthropic's Claude (Opus 4.8, 1M-context) via the Claude Code CLI. See the `Co-Authored-By` trailer on every commit.

---

Documentation drift policy: if content does not match reality — stale phase status, removed commands, broken links — open a PR titled `docs: fix README drift — <area>`. Drift-prone phrasing (enumerated counts, enumerated incident lists) is actively avoided; flag any regression toward that pattern in PR review.
