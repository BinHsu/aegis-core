# 🗺️ Aegis Core (V2) — Roadmap

**Current Status**: Architecture design complete; implementation bootstrapping pending.
**Last Updated**: 2026-04-11

This roadmap reflects the architectural decisions captured in
`ARCHITECTURE.md` and the ADRs in `docs/adr/`. Before working on any
phase, read:

1. `CLAUDE.md` — AI agent rules and ironclad engineering conventions.
2. `ARCHITECTURE.md` — system topology and governance.
3. `docs/adr/` — every accepted architecture decision.
4. `docs/threat-model.md` — threat assumptions driving each phase.

All phases respect the **"Clone it, build it, it just works"** ethos and
the privacy posture defined in `ARCHITECTURE.md` §9.

---

## Phase 0: Governance Scaffolding

> *"Get the rails in place before writing any engine code."*

- [x] Establish `docs/adr/` directory and ADR-0001–0012
- [x] Write `ARCHITECTURE.md` §9 Data Governance & Privacy
- [x] Write `ARCHITECTURE.md` §10 Secure SDLC & Supply Chain
- [x] Write `ARCHITECTURE.md` §11 Known Limitations
- [x] Write `docs/threat-model.md` STRIDE skeleton
- [x] Write `SECURITY.md` with responsible disclosure and privacy commitments
- [x] Add `CODEOWNERS` file naming the architecture owner per subtree
- [x] Add `CONTRIBUTING.md` with development setup and PR conventions
- [x] Add `.pre-commit-config.yaml` with `gitleaks`, `clang-format`, `gofmt`, `prettier`, `buf lint`
- [x] Add `.gitignore` covering build artifacts, `.bazel_cache/`, `.venv/`, `node_modules/`, `/models/*.gguf`
- [x] Add minimal GitHub Actions CI: lint + secret scan + `buf lint` (no builds yet)
- [x] Enable GitHub branch protection on `main`: required reviewers, required CI, no force-push, signed commits — see `docs/github-setup.md` (SSH signing setup in §0.5)
- [x] Enable GitHub **Private Vulnerability Reporting** (per SECURITY.md) — see `docs/github-setup.md`
- [x] Enable GitHub **secret scanning with push protection** — see `docs/github-setup.md`
- [x] Document `/models/manifest.json` schema for model provenance (ARCH §10.1)

---

## Phase 1: The Bazel Monorepo Engine

> *"One command, one build, one toolchain — no global pollution."*

### Build System
- [x] Initialize Bazel `MODULE.bazel` (bzlmod) with `rules_proto`, `protobuf` — Session 1 baseline
- [x] Add `rules_cc`, `grpc`, `apple_support` bazel_deps — Session 2 (cold grpc build 5m14s)
- [x] Add `rules_foreign_cc` 0.15.1 + whisper.cpp v1.8.4 via http_archive+SHA256, CMake build — Session 4a (cold +7m38s)
- [x] Add `rules_go` 0.52.0 + `gazelle` 0.50.0 bazel_deps + Go 1.24.12 hermetic SDK — Session 5
- [ ] Add `rules_nodejs`, `rules_rust`, `rules_oci` bazel_deps — Phase 3/4
- [x] Configure `.bazelrc` with CPU default, metal/cuda/cpu configs, `try-import %workspace%/.bazelrc.user` (ADR-0009)
- [x] `.bazelversion` pinning Bazel 7.4.1
- [x] Add `tools/bazelisk/bazelisk` wrapper — downloads bazelisk locally, forces `--output_user_root=./.bazel_cache` (CLAUDE.md Rule 6)
- [x] Configure hermetic C++ toolchain with Metal (macOS) / CUDA (Linux) selection via `--config=metal|cuda|cpu` and `tools/scripts/configure_backend.sh` (ADR-0009 Sub-decision 4) — Session 2/4a
- [x] Configure hermetic Go toolchain (Go 1.24.12 SDK via `go_sdk.download` extension) — Session 5
- [ ] Configure hermetic Node.js toolchain for frontend — Phase 4 (frontend currently NPM-managed per Phase 1 C1; Bazel wrap deferred to Phase 4a OCI packaging)

### Contracts
- [x] Create `proto/aegis/v1/aegis.proto` (per ADR-0008 layout) and define: `CreateMeeting`, `StreamTranscribe`, `JoinAsViewer`, `EndMeeting` (AskRAG removed per ADR-0012)
- [x] Define `IngestMessage` oneof (`PcmChunk` | `ControlEvent{PAUSE|RESUME|END_STREAM}`) per ADR-0006
- [x] Define `CreateMeetingRequest` with reserved field for Phase 5 `allowed_viewer_account_ids` per ADR-0001
- [x] Add `buf` configuration (`proto/buf.yaml`) and `buf breaking` check to CI
- [x] **Validate** `proto/aegis/v1/BUILD.bazel` — `proto_library` target builds (Session 1)
- [x] Generate C++ bindings under Bazel (`cc_proto_library` + `cc_grpc_library`) — Session 2 complete
- [x] Generate Go bindings under Bazel (`go_proto_library` with `go_proto` + `go_grpc_v2` compilers) — Session 5 complete
- [ ] Generate TypeScript bindings under Bazel — Phase 3

### C++ Engine Skeleton
- [x] `engine_cpp/` directory + BUILD.bazel per ADR-0008 layout — Session 3
- [x] Minimal gRPC server in `engine_cpp/cmd/engine/` that starts and serves Health (Session 3)
- [x] Implement `SensitiveBytes` type with redacting `operator<<` (ADR-0005 R3) — Session 3
- [x] Implement `ResourceBudget` with atomic Reserve/Release, fail-fast RESOURCE_EXHAUSTED (ADR-0010) — Session 3
- [x] `#ifdef AEGIS_DEV_AUDIO_DUMP` gating for debug dump code (ADR-0005 R7) — Session 3 (verified compiled out of release)
- [x] Vendor `whisper.cpp` via Bazel `http_archive` + SHA256 and rules_foreign_cc — Session 4a
- [x] Engine binary links whisper.cpp + ggml; `whisper_print_system_info` at startup proves linkage (CPU baseline, Apple NEON/Accelerate detected) — Session 4a
- [x] `WhisperEngine::Create(model_path)` + `Transcribe(pcm)` returning `absl::StatusOr<T>`, with gtest unit tests for error paths — Session 4b
- [x] End-to-end transcription integration test with `ggml-tiny.en.bin` + `samples/jfk.wav`; WAV reader + download_models.sh + manifest.json real SHA-256 — Session 4c
- [x] Wire `WhisperEngine` into `StreamTranscribe` consuming `IngestMessage` stream with full state machine (WaitingForStart → Active → Paused/Resumed → END_STREAM); per-session `WhisperEngine`; `ResourceBudget` Reserve/Release paired with session lifetime; in-process gRPC integration test passing end-to-end — Session 4d
- [x] Session lifetime management (per-session WhisperEngine, accumulated PCM ring buffer, Pause/Resume/END_STREAM state machine) — Session 4d (`engine_cpp/src/session/session.cc`)

### Go Gateway Skeleton
- [x] Establish `gateway_go/` with Go module and Bazel build (rules_go) — Session 5
- [x] HTTP/2 gRPC server acting as passthrough to C++ engine — Phase 2 A1 (`/healthz` proxies `Engine.Health`)
- [ ] Implement `RedactedPCM` type with safe log formatter (ADR-0005 R3) — Phase 2 A3 (when PCM actually flows through Go)
- [ ] Implement Hexagonal Architecture interface boundaries for auth, storage, telemetry (ARCH §5) — Phase 2 A2+

### Models
- [x] Create `/models/` directory with `manifest.json` and download script that verifies SHA256 before mmap (ARCH §10.1) — Phase 0 / Session 4c (`tools/scripts/download_models.sh`)
- [x] Document the first model set: `whisper-tiny-en` (Phase 1 test fixture), `whisper-large-v3-turbo-q4` (production placeholder), diarization embedder (placeholder), sentence-transformers (placeholder), llama-3-8b (Phase 5+) — `models/manifest.json`

---

## Phase 2: Internal MVP & The BFF

> *"Raw audio in, transcript out, no UI yet."*

### Gateway Functionality
- [x] Go GW: gRPC client to engine; `/healthz` aggregates gateway + engine status (Phase 2 A1)
- [x] Proto codegen distribution strategy (ADR-0013): checked-in `.pb.go` under `gateway_go/gen/go/` via `buf generate`; Bazel still authoritative; CI drift check via `proto_gen.sh + git diff --exit-code`
- [ ] Go GW: implement Pion WebRTC to accept browser UDP frames
- [ ] Go GW: implement `gRPC-Web` multiplexing for cloud-mode viewer transport
- [x] Go GW: implement **WebSocket + Protobuf** transport for local-mode viewer (ADR-0007) — Phase 2 A5 (`/ws/viewer?session_id=&token=`, binary `aegis.v1.ViewerEvent` frames, `Sec-WebSocket-Protocol: aegis.v1.transcript`, shares Registry+Issuer with the gRPC Gateway)
- [x] Go GW: implement session registry (ADR-0004 `Session` struct) — in-memory, per-replica (Phase 2 A2). Subscribe/Broadcast fan-out added in Phase 2 A5 (per-subscription sequence, slow-consumer drop policy).
- [ ] Go GW: implement `ControlEvent{PAUSE|RESUME|END_STREAM}` generation on WebRTC state transitions (ADR-0006)
- [ ] Go GW: configure keepalive — 30s Time / 10s Timeout for both gRPC to C++ and gRPC-Web to viewers (ADR-0006)
- [x] Go GW: implement JWT session-token issuance and verification (ADR-0001) — Phase 2 A2 (HS256, process-scoped key, alg=none rejected, cross-session replay rejected)
- [x] Go GW: implement `aegis.v1.Gateway` server: CreateMeeting + EndMeeting full; JoinAsViewer real fan-out via `Session.Subscribe` (Phase 2 A5, replacing A2 stub); NegotiateWebRTC UNIMPLEMENTED until A3
- [ ] Go GW: implement graceful shutdown with `terminationGracePeriodSeconds: 14400` matching `session_max_lifetime` (ADR-0006)

### Dual-Mode Wiring
- [ ] Local mode: implement `bazel run //:app_local` that starts Go GW and spawns C++ engine as child (ARCH §5)
- [ ] Local mode: bind Go GW to 0.0.0.0 for LAN viewers (ADR-0007)
- [ ] Local mode: dummy auth middleware (ARCH §8 Local Mode Interface Fallback)
- [ ] Cloud mode: Cognito JWT middleware
- [ ] Cloud mode: Pod Identity integration scaffolding

### Testing
- [ ] Unit tests: C++ (`gtest`), Go (`go test`)
- [ ] Integration test: send raw WAV files through Go GW and verify C++ transcriptions are streamed back
- [ ] **WER golden audio regression suite** — 10–20 fixtures in English, Traditional Chinese, code-switch, multi-speaker, noise; WER threshold enforced in CI (ARCH §10.5)
- [ ] `buf breaking` check on every proto change
- [ ] Load test scaffolding: k6 driving N concurrent WebRTC sessions (nightly)

---

## Phase 3: The Frontlines (Pure Web React + Vite)

> *"Ship a usable product on web first; Tauri is not on this phase's critical path."*

**Scope change from original roadmap**: Phase 3 delivers **pure web only**. Tauri is deferred per ADR-0002 and ADR-0003.

### Frontend Scaffolding
- [x] Scaffold `frontend_web/` with React 19 + Vite 6 + TypeScript 5.7 strict — Phase 1 C1 (NPM-managed; Bazel wrap deferred to Phase 4a)
- [ ] Configure generated Protobuf JS/TS bindings under Bazel
- [x] `AudioCaptureProvider` interface + `WebAudioCaptureProvider` impl (getUserMedia, getDisplayMedia, Web Audio mixing for the three capture modes per ADR-0003) — Phase 1 C2
- [x] `TranscriptStreamProvider` interface + `GrpcWebTranscriptStreamProvider` (Cloud) + `WebSocketTranscriptStreamProvider` (Local) stubs per ADR-0007 — Phase 1 C3
- [ ] `AuthProvider`, `FileSystemProvider`, `NotificationProvider`, `AutoUpdateProvider` stubs — Phase 2+
- [ ] Respect all ADR-0002 Phase 3 Constraints 1–6 (no `chrome.*`, no Service Worker dependency, etc.)

### Host UI (Staff)
- [ ] Login flow (Cognito Cloud / dummy Local) — Phase 2
- [ ] "New Meeting" flow: RAG corpus selector → `CreateMeeting` RPC → session token display — Phase 2
- [x] Audio source picker: "Physical room (microphone)" vs "Remote meeting (browser tab)" vs both — Phase 1 C4
- [x] `getUserMedia` and `getDisplayMedia` calls with clear privacy copy (ADR-0003) — Phase 1 C4 (via WebAudioCaptureProvider)
- [ ] One-time audio-processing consent capture on first use (ARCH §9.3; no biometric consent needed — see ADR-0012)
- [ ] Speaker label tagging UI — curated choice list, **no free-text name input** (ARCH §9.2)
- [ ] Live prompter display with rolling 5-line window
- [ ] Export flow: Markdown + JSON download
- [ ] "End Meeting" button
- [ ] QR code display for LAN viewer join (Local mode only) (ADR-0007)

### Viewer UI (Boss)
- [x] Join via invite URL → token parsing → `TranscriptStreamProvider` subscription — Phase 1 C4
- [x] Rolling 5-line prompter display — Phase 1 C4 (PROMPTER_WINDOW=5)
- [x] "Host reconnecting..." banner on transient host loss (ADR-0006 Disconnected state) — Phase 1 C4
- [x] "Meeting ended" message on session termination — Phase 1 C4
- [x] **No export UI** (L3) — Phase 1 C4 (intentionally absent)
- [x] No history rendering for late joiners (L4 is a feature, not a bug) — Phase 1 C4 (rolling window only)

### Cross-WebView Testing
- [ ] Chrome / Edge primary testing
- [ ] WKWebView sanity check on macOS (ensures Phase 4 Tauri wrap will not be blocked)
- [ ] Firefox / Safari explicitly NOT supported for host role (L6); document in README

---

## Phase 4: SRE & Cloud Orchestration

> *"Make it deployable. Sign it. Roll it out safely."*

### Phase 4a: Package
- [ ] Bazel `rules_oci`: package C++ engine, Go GW, and frontend into Distroless OCI images
- [ ] Each image runs as non-root with dropped capabilities and read-only root filesystem except tmpfs mounts
- [ ] Image tagging convention: `prod-<semver>-<git_sha>`, `staging-<git_sha>`, `dev-<git_sha>`
- [ ] Produce SBOMs (Syft / CycloneDX) alongside every image (ARCH §10.1)

### Phase 4b: Sign & Scan
- [ ] Cosign / Sigstore signing in GitHub Actions using OIDC (ARCH §10.1)
- [ ] SLSA Level 3 provenance emission
- [ ] Trivy container scan; block push on critical CVEs
- [ ] kube-score + kube-bench manifest scan
- [ ] Checkov IaC scanner for K8s manifests + Dockerfile + Helm charts (complements kube-score/kube-bench from the misconfiguration / policy-as-code angle; see debrief discussion 2026-04-12)
- [ ] CodeQL, Semgrep, gosec, govulncheck, clang-tidy in CI (ARCH §10.2)
- [ ] Verify no binary contains `AEGIS_DEV_AUDIO_DUMP` symbol (ADR-0005 R7)
- [ ] ECR push pipeline; ArgoCD in landing-zone repository polls the manifests in this repository

### Phase 4c: Progressive Delivery
- [ ] Argo Rollouts or Flagger integration in EKS manifests
- [ ] SLO-based canary gates (ARCH §10.4)
- [ ] Automatic rollback on error budget burn >25%
- [ ] Graceful shutdown verified end-to-end under rolling update (ADR-0006)
- [ ] Audio-namespace Kyverno / Gatekeeper policies (ADR-0005 R6): reject PVC, reject hostPath
- [ ] Velero backup schedule explicitly excludes `aegis-audio` namespace (ADR-0005 R6)

### Phase 4d: Observability Wiring + Build Cache Decision

- [ ] Pick a Bazel remote cache strategy per ADR-0014 (Option α `actions/cache` only / β BuildBuddy SaaS / γ self-hosted bazel-remote). Decision deferred to whichever of T1–T6 trigger conditions fires first; this checklist item flips to checked once the chosen ADR-0014 option is wired and the ADR is updated with the resolving commit.
- [ ] OTLP exporter to X-Ray / Tempo in Cloud, stdout in Local (ARCH §8)
- [ ] Custom `SpanProcessor` enforcing attribute allowlist (ADR-0005 R4)
- [ ] Structured JSON logs via FluentBit in Cloud
- [ ] Grafana dashboards and PagerDuty alerts provisioned by landing-zone repository
- [ ] `aegis_host_transient_loss_total`, `aegis_questions_detected_total`, `aegis_hints_emitted_total`, and other domain metrics emitted

---

## Phase 5: Hardening & Compliance

> *"Validate everything you believed when you were designing."*

### Privacy / Security Validation
- [ ] External penetration test against staging
- [ ] External privacy-engineering review (BIPA, CCPA, GDPR) prior to EU / Illinois customer onboarding
- [ ] DPIA (Data Protection Impact Assessment) under GDPR Art. 35
- [ ] LINDDUN privacy threat modeling as complement to STRIDE
- [ ] Threat model review by external security consultant

### Resilience
- [ ] Chaos experiments: controlled pod kills, network partitions, resource starvation
- [ ] Disaster recovery game day; document RPO / RTO
- [ ] Validate L1 / L2 behavior end-to-end under real failure conditions

### Compliance
- [ ] SOC 2 Type 1 audit (assuming the organization decides to pursue — open question, see ARCH §10 SLO gates)
- [ ] Evidence collection automation for audit controls
- [ ] Published control mapping: SOC 2 CC series → Aegis implementation points

### Ergonomics & Feature Extensions
- [ ] **ADR-0002 future extension**: per-account viewer allowlist via `allowed_viewer_account_ids`
- [ ] **ADR-0007 future extension**: mDNS / Bonjour auto-discovery for Local mode
- [ ] **Tauri shell** (per ADR-0002) for users needing native meeting app audio capture — gated on real customer demand
- [ ] Optional host-side crash-safe local persistence (L1 mitigation, opt-in)
- [ ] Regional routing for EU / data residency (deferred from MVP)
- [ ] Consent ledger maturation: append-only S3 WORM mirror, independent backup, legal-hold workflow

### Compliance SKU (Conditional)
- [ ] **IF** regulated-industry customers (FINRA, HIPAA) materialize **AND** audit / legal review approves, design the opt-in **Compliance Archival SKU** — a fourth data layer allowing opt-in audio persistence with S3 Object Lock, explicit consent from all participants, and legal-hold support. This is a discrete Phase 5+ decision with its own ADR, not a given.

---

## Open Decisions Carried from Phase 0 Design

These were deliberately deferred during architecture design. Each should
be answered before or during the phase indicated.

| Decision | Target Phase | Reference |
|---|---|---|
| SOC 2 Type 1 vs Type 2 as first target | Late Phase 4 | ARCH §10 |
| EU / data residency support | Phase 5 | ADR-0004 |
| mDNS LAN discovery vs QR code only | Phase 5 | ADR-0007 |
| Tauri shell implementation | Phase 5 (demand-driven) | ADR-0002 |
| Consent ledger retention / legal-hold workflow | Phase 5 | ARCH §9.3 |
| Compliance Archival SKU | Phase 5+ (customer-driven) | ARCH §9.1 |

---

## How Phases Relate to the Ironclad Rules

Every phase respects CLAUDE.md:

- **Rule 1 Honesty**: phase checklists are not marked done unless actually done. AI agents updating this file MUST be truthful about phase completion.
- **Rule 2 Testing Integrity**: no phase ships without real tests producing verifiable outputs. The WER golden suite (Phase 2) is the primary example of this rule in action.
- **Rule 3 Doc Sync**: every checked-off item must correspond to code changes and any architectural implications must be reflected in `ARCHITECTURE.md` or a new ADR.
- **Rule 4 Tech Boundaries**: no new languages or frameworks beyond TypeScript/React, Go, C++, Rust (Phase 5+ Tauri). Any proposal to deviate requires a new ADR first.
- **Rule 5 Language**: all code, docs, and ADRs in English.
- **Rule 6 Directory Confinement**: all dependencies, caches, and models stay inside the repository tree. Bazel output goes to `./.bazel_cache`; models to `./models`; Python (if any) to `./.venv`.
