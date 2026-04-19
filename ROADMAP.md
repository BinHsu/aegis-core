# 🗺️ Aegis Core (V2) — Roadmap

**Current Status**: Architecture design complete; implementation bootstrapping pending.
**Last Updated**: 2026-04-18 (Phase 3c exited — Slices 1-6 landed, 8-job CI matrix adds Playwright chromium + webkit live-browser gate)

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
- [x] Add `rules_nodejs` (via `aspect_rules_js`) bazel_dep — Phase 3 (ADR-0015)
- [x] Add `rules_oci` + `rules_pkg` bazel_deps — Phase 4a Slice 1 (ADR-0025)
- [ ] Add `rules_rust` bazel_dep — Phase 4 Tauri
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
- [x] Implement `RedactedPCM` type with safe log formatter (ADR-0005 R3) — Phase 2 A4 follow-up. `gateway_go/internal/sensitive.RedactedPCM` implements `fmt.Formatter` (covers every verb), `fmt.Stringer`, `fmt.GoStringer`, `json.Marshaler`, and `slog.LogValuer` — all render `[REDACTED PCM N bytes]` matching the C++ `SensitiveBytes` format for cross-runtime grep parity. `Pipeline.WritePCM` now takes `sensitive.RedactedPCM` (compile-time guard); `.Bytes()` is the single ADR-0005-audited unwrap at the proto-Send boundary.
- [~] Implement Hexagonal Architecture interface boundaries for auth, storage, telemetry (ARCH §5) — Phase 2 A2+. **Auth port landed** (`gateway_go/internal/auth.Provider`, NoOp + StaticJWT implementations, interceptor wiring). **Storage / Telemetry ports descoped** — no callers today, adding them speculatively would be YAGNI; see Phase 2 Known Gaps for when each port's first caller is expected to arrive.

### Models
- [x] Create `/models/` directory with `manifest.json` and download script that verifies SHA256 before mmap (ARCH §10.1) — Phase 0 / Session 4c (`tools/scripts/download_models.sh`)
- [x] Document the first model set: `whisper-tiny-en` (Phase 1 test fixture), `whisper-large-v3-turbo-q4` (production placeholder), diarization embedder (placeholder), sentence-transformers (placeholder), llama-3-8b (Phase 5+) — `models/manifest.json`

---

## Phase 2: Internal MVP & The BFF

> *"Raw audio in, transcript out, no UI yet."*

### Gateway Functionality
- [x] Go GW: gRPC client to engine; `/healthz` aggregates gateway + engine status (Phase 2 A1)
- [x] Proto codegen distribution strategy (ADR-0013): checked-in `.pb.go` under `gateway_go/gen/go/` via `buf generate`; Bazel still authoritative; CI drift check via `proto_gen.sh + git diff --exit-code`
- [x] Go GW: implement Pion WebRTC to accept browser UDP frames — non-trickle SDP exchange via `internal/webrtc.Negotiator` (Phase 2 A3); loopback ICE+RTP test passing
- [x] Go GW: wire full audio pipeline (WebRTC → forward Opus verbatim → engine `StreamTranscribe` → engine-side libopus decode → `Session.Broadcast`) via `internal/pipeline.Pipeline` and factory-injected `AudioPipelineStart` (Phase 2 A4 originally landed with gateway-side pion/opus decode; Phase 3 LAN-phone testing exposed pion/opus's mode-3 gap, refactored to engine-side decode per [ADR-0016](docs/adr/0016-opus-decode-on-engine.md)); bufconn unit tests + `AEGIS_ENGINE_ADDR`-gated integration test
- [x] Go GW: implement `gRPC-Web` multiplexing for cloud-mode viewer transport — `github.com/improbable-eng/grpc-web` v0.15.0 wrapped around the same `grpc.Server` instance; the HTTP listener (`:8080`) sniffs `IsGrpcWebRequest` / `IsAcceptableGrpcCorsRequest` and routes matching traffic to the wrapped server, leaving `/healthz` and `/ws/viewer` on the native HTTP mux. Origin allowlist is permissive for Local mode (ADR-0007 LAN scope); Cloud mode will tighten via a config-driven origin list (see Phase 2 Known Gaps).
- [x] Go GW: implement **WebSocket + Protobuf** transport for local-mode viewer (ADR-0007) — Phase 2 A5 (`/ws/viewer?session_id=&token=`, binary `aegis.v1.ViewerEvent` frames, `Sec-WebSocket-Protocol: aegis.v1.transcript`, shares Registry+Issuer with the gRPC Gateway)
- [x] Go GW: implement session registry (ADR-0004 `Session` struct) — in-memory, per-replica (Phase 2 A2). Subscribe/Broadcast fan-out added in Phase 2 A5 (per-subscription sequence, slow-consumer drop policy).
- [x] Go GW: implement `ControlEvent{PAUSE|RESUME|END_STREAM}` generation on WebRTC state transitions (ADR-0006) — Phase 2 A4 (ICE `Disconnected`→PAUSE, `Connected`→RESUME, `Failed`→END_STREAM, translated inside `cmd/gateway` factory closure from `webrtc.Negotiator.ICEChan`)
- [x] Go GW: configure keepalive — 30s Time / 10s Timeout for both gRPC to C++ and gRPC-Web to viewers (ADR-0006) — server-side `keepalive.ServerParameters{Time: 30s, Timeout: 10s}` + `keepalive.EnforcementPolicy{MinTime: 10s, PermitWithoutStream: true}` on the `aegis.v1.Gateway` server; matching `keepalive.ClientParameters` on the engine dial client. Named constants in `cmd/gateway/main.go` so ADR-0006 stays the single source of truth. Detection window ≤ 40 s for silent viewer disconnects (laptop lid closed, cable pulled) so fan-out channels are reclaimed promptly.
- [x] Go GW: implement JWT session-token issuance and verification (ADR-0001) — Phase 2 A2 (HS256, process-scoped key, alg=none rejected, cross-session replay rejected)
- [x] Go GW: implement `aegis.v1.Gateway` server: CreateMeeting + EndMeeting full; JoinAsViewer real fan-out via `Session.Subscribe` (Phase 2 A5, replacing A2 stub); NegotiateWebRTC real SDP exchange (Phase 2 A3)
- [x] Go GW: implement graceful shutdown with `terminationGracePeriodSeconds: 14400` matching `session_max_lifetime` (ADR-0006) — configurable via `AEGIS_GATEWAY_DRAIN_TIMEOUT` env var (duration parseable by `time.ParseDuration`), default 30 s for Local-mode developer feel. Cloud deployment sets it to 14400 s (4 h) to match the K8s `terminationGracePeriodSeconds` and ADR-0001 `session_max_lifetime`. Shutdown races `grpcSrv.GracefulStop()` against `shutdownCtx`; on timeout the forced `grpcSrv.Stop()` fallback guarantees the drain window is a hard UPPER bound, not a lower bound. HTTP server shuts first (fast), then gRPC.

### Dual-Mode Wiring
- [x] Local mode: implement `bazel run //:app_local` that starts Go GW and spawns C++ engine as child (ARCH §5) — `gateway_go/cmd/app_local/main.go` locates both binaries via Bazel runfiles, polls engine Health until Ready before starting the gateway, prefixes interleaved child stdout/stderr with `[engine]`/`[gateway]`, and on SIGINT/SIGTERM tears down the gateway first (drain) then the engine; root alias `//:app_local` in `BUILD.bazel`.
- [x] Local mode: bind Go GW to 0.0.0.0 for LAN viewers (ADR-0007) — default HTTP/gRPC bind is `":8080"` / `":9090"` which Go's `net.Listen` resolves to `0.0.0.0:port` (bind on all interfaces) per stdlib convention. Explicit comment in `cmd/gateway/main.go` documents WHY — to prevent a well-meaning maintainer from narrowing to `localhost` thinking it's a security improvement (it would break the LAN QR-code viewer flow the ADR calls out).
- [x] Local mode: dummy auth middleware (ARCH §8 Local Mode Interface Fallback) — `gateway_go/internal/auth.NoOpProvider` plus `auth.UnaryInterceptor` / `auth.StreamInterceptor` wired into the gRPC server. Every RPC gets the synthetic `Principal{UserID: "local", TenantID: "", Mode: ModeLocal}` in ctx via `auth.WithPrincipal`; handlers that care (tenant-scoped queries, host-only policy) read it via `auth.FromContext`.
- [~] Cloud mode: Cognito JWT middleware — **SCAFFOLDED, NOT production-wired.** `auth.StaticJWTProvider` validates HS256-signed Bearer tokens against a pre-shared secret (integration-test grade). Validates signing method (rejects `alg=none` downgrade attacks per its dedicated test), `aud` / `exp`, and extracts `sub` + `custom:tenant_id` claims into the Principal. Live Cognito JWKS fetching is Phase 3 scope — see Phase 2 Known Gaps.
- [~] Cloud mode: Pod Identity integration scaffolding — **DESCOPED from Phase 2.** No AWS callers exist yet (no DynamoDB / S3 / SQS). Will wire `github.com/aws/aws-sdk-go-v2/config.LoadDefaultConfig` when Phase 4 packaging adds the EKS deployment surface — Pod Identity credentials flow via the service-account IAM role with zero Go code changes at that point. See Phase 2 Known Gaps.

### Testing
- [ ] Unit tests: C++ (`gtest`), Go (`go test`)
- [x] Integration test: send raw WAV files through Go GW and verify C++ transcriptions are streamed back — Phase 2 A4 follow-up; `TestTranscribeJFKLiveEngine` in `gateway_go/internal/pipeline/pipeline_test.go` gated on `AEGIS_ENGINE_ADDR`, feeds `@whisper_cpp//:samples/jfk.wav` through `Pipeline.WritePCM`, asserts transcript contains "ask not" / "your country" (parity with `engine_cpp/tests/integration/stream_transcribe_test.cc`).
- [~] **WER golden audio regression suite** — 10–20 fixtures in English, Traditional Chinese, code-switch, multi-speaker, noise; WER threshold enforced in CI (ARCH §10.5). **DESCOPED from Phase 2 — see "Known Gaps" below.** In place of a full regression suite, `TestTranscribeJFKLiveEngine` (see above) acts as a single-fixture English smoke test: it gates on transcript content equality, not WER, which is sufficient to catch catastrophic regressions (model file corruption, resampling off-by-factor, wrong decoder) but **NOT** sufficient to catch subtle accuracy drift.
- [x] `buf breaking` check on every proto change — configured in `.pre-commit-config.yaml:98-104` using `bufbuild/buf` v1.67.0 `buf-breaking` hook against `main`; runs on every commit. `buf.yaml` uses `FILE` breaking rules with a single exception (`FILE_SAME_GO_PACKAGE`, rationale inline in that file) because this project is the only consumer of its own protos (ADR-0013).
- [ ] Load test scaffolding: k6 driving N concurrent WebRTC sessions (nightly)

### Known Gaps (Phase 2 — tracked here so new contributors see them)

> *Phase 2's stated scope is "wire it up to the point it works."* The
> items below are **deliberately** shallow or absent, not forgotten.
> Each entry states the gap, why it was descoped, and what a future
> contributor needs to know before depending on the affected path.

- **No WER regression suite.** The canonical measure of ASR quality is Word
  Error Rate on a curated corpus — 10–20 fixtures spanning English, Traditional
  Chinese, code-switch, multi-speaker, and noise conditions, with a CI
  threshold (e.g. `WER_EN < 0.08`) that fails the build if accuracy drifts.
  This repo ships without one. **Why descoped:**
    1. *Ground truth sourcing is expensive.* Can't use YouTube (licensing) or
       random podcasts (CC-BY-NC disallows commercial use). Public-domain
       options (LibriSpeech, Common Voice) cover English well but thin out on
       Taiwan-accented Traditional Chinese, which is a primary target language
       here (ADR-0012 "English + Traditional Chinese baseline").
    2. *Ground truth quality needs human audit.* Hand-written transcripts with
       a second-pass reviewer, per language. Whisper output cannot be used as
       ground truth — that's circular.
    3. *Harness isn't the hard part.* The hard part is the corpus; `jiwer` /
       `sacrebleu` give us WER arithmetic, punctuation normalization, and
       case-folding out of the box on the Python side (Go equivalents are
       thinner but tractable). What we lack is the audio.
  **Stopgap in place:** `TestTranscribeJFKLiveEngine` (see Testing above)
  asserts transcript content equality on one English fixture (`jfk.wav`).
  That catches whole-chain regressions (wrong model, broken resampling,
  corrupted Opus decode) but NOT subtle WER drift. If someone swaps the
  whisper model or tweaks Opus params, this gap means the quality regression
  will only surface in user reports, not CI.
  **How to close it:** pick the corpus, commission / record the audio,
  land ground-truth transcripts, then the harness is a 1-day wrap. Phase 3+
  territory, likely paired with the first real users' consent to use
  their meeting recordings as anchor fixtures.

- **Cognito JWT middleware is stubbed, not production-wired.** The
  `internal/auth` package lands a real `AuthProvider` port, a working
  `NoOpProvider` for Local mode, and a `StaticJWTProvider` stub for Cloud
  mode that validates HS256 (shared-secret) or RS256-with-hardcoded-JWKS
  tokens. **What's missing:** live JWKS fetching from a Cognito User Pool
  (`https://cognito-idp.{region}.amazonaws.com/{pool}/.well-known/jwks.json`),
  JWKS cache with periodic refresh, and mapping Cognito claims
  (`cognito:groups`, `custom:tenant_id`) to the internal `Principal`.
  **Why descoped:** real AWS Cognito wiring needs a live User Pool to
  test against, which is a Phase 3 frontend-login concern — the first
  caller is the React login flow (Phase 3 ROADMAP line 143). Writing the
  JWKS client now without a caller exercising it is speculative. **How to
  close:** drop in `github.com/lestrrat-go/jwx/v2` (JWKS client +
  caching), point it at the real User Pool URL (config-driven), and wire
  claim extraction — ~half a day once the Cognito pool exists.

- **Pod Identity integration is descoped from Phase 2 entirely.** ADR-0001
  says the Gateway authenticates to DynamoDB/S3 via EKS Pod Identity
  (IRSA's successor). **Why descoped:** Phase 2 has zero AWS callers —
  no DynamoDB, no S3, no SQS. Pod Identity's only value is when you're
  actually making AWS API calls. **How to close:** when Phase 4 OCI
  packaging adds the EKS deployment surface from the
  `aegis-aws-landing-zone` repo, wire
  `github.com/aws/aws-sdk-go-v2/config.LoadDefaultConfig` — it picks up
  Pod Identity credentials via the service-account IAM role without any
  Go-side code changes. Tracking this as Phase 4 ops wiring, not a Go
  code task.

- **No production-grade load test cadence.** A `k6` skeleton script will
  land in `tests/load/` with a single scenario (N concurrent
  WebSocket viewers fan-out), runnable via `make load-smoke`. That
  exercises "it doesn't crash under 100 connections." **What's missing:**
  sustained soak, p99 latency SLO gates, WebRTC host simulation (requires
  a browser or pion-driven harness), multi-region egress shaping, CI
  cadence. Phase 4 operational concern — deploy surface arrives with real
  infrastructure, not before.

- **Hexagonal ports partially landed.** ARCH §5 calls out auth, storage,
  telemetry, and filesystem as the ports. Phase 2 lands `AuthProvider`
  (real) and stops. `StorageProvider` and `TelemetryProvider` have zero
  callers today (no persisted meeting metadata, no OpenTelemetry
  shipper), so writing those ports now would be pure speculation.
  **How to close:** introduce each port when its first caller arrives —
  `StorageProvider` when we persist meeting records (Phase 3+),
  `TelemetryProvider` when we add OTel shippers (Phase 4+
  observability sprint).

---

## Phase 3: The Frontlines (Pure Web React + Vite)

> *"Ship a usable product on web first; Tauri is not on this phase's critical path."*

**Scope change from original roadmap**: Phase 3 delivers **pure web only**. Tauri is deferred per ADR-0002 and ADR-0003.

**Sub-phase split (2026-04-15)**: platform foundations (3a) outgrew the original frontend-only scope once Phase 3 live-phone testing surfaced ADR-0016 and the engine's inference story sharpened through ADRs 0017–0020. Host UI + cross-WebView acceptance move to 3c, gated by 3b's engine inference implementation. Same a/b/c/d convention as Phase 4.

### Phase 3a: Platform + architecture foundations ✅ (2026-04-13 → 2026-04-15)

#### Frontend scaffolding (shipped)
- [x] Scaffold `frontend_web/` with React 19 + Vite 6 + TypeScript 5.7 strict — Phase 1 C1; **Phase 3 promoted from local-npm to hermetic Node + pnpm via `aspect_rules_js`** (ADR-0015). Wrapper script `./tools/scripts/frontend.sh {install|dev|build|typecheck}` is the single entry point; no system `node` required.
- [x] Configure generated Protobuf JS/TS bindings — Phase 3 kickoff. `buf.gen.yaml` adds `protobuf-es` v1 + `connectrpc/es` v1 plugins; output checked in to `frontend_web/src/gen/proto/aegis/v1/{aegis_pb.ts,aegis_connect.ts}` per ADR-0013 distribution model. Connect's grpc-web transport speaks to the gateway's `improbable-eng/grpc-web` wrapper. (v2 migration deferred — see plugin-version comment in buf.gen.yaml.)
- [x] `AudioCaptureProvider` interface + `WebAudioCaptureProvider` impl (getUserMedia, getDisplayMedia, Web Audio mixing for the three capture modes per ADR-0003) — Phase 1 C2
- [x] `TranscriptStreamProvider` interface + `GrpcWebTranscriptStreamProvider` (Cloud) + `WebSocketTranscriptStreamProvider` (Local) stubs per ADR-0007 — Phase 1 C3
- [x] Audio source picker: "Physical room (microphone)" vs "Remote meeting (browser tab)" vs both — Phase 1 C4
- [x] `getUserMedia` and `getDisplayMedia` calls with clear privacy copy (ADR-0003) — Phase 1 C4 (via WebAudioCaptureProvider)

#### Viewer UI (Boss) — complete
- [x] Join via invite URL → token parsing → `TranscriptStreamProvider` subscription — Phase 1 C4
- [x] Rolling 5-line prompter display — Phase 1 C4 (PROMPTER_WINDOW=5)
- [x] "Host reconnecting..." banner on transient host loss (ADR-0006 Disconnected state) — Phase 1 C4
- [x] "Meeting ended" message on session termination — Phase 1 C4
- [x] **No export UI** (L3) — Phase 1 C4 (intentionally absent)
- [x] No history rendering for late joiners (L4 is a feature, not a bug) — Phase 1 C4 (rolling window only)

#### Platform + architecture (2026-04-13 → 2026-04-15)
- [x] LAN demo reach + gateway N:N dial wiring — commit `69c92bd`
- [x] **ADR-0015** Hermetic Node.js via `aspect_rules_js`
- [x] **ADR-0016** Opus decode moves from gateway (pion/opus) to engine (libopus) — 4 commits (day 1 infra + day 2a engine `kOpus` + day 2b gateway emits `OpusChunk` + day 2c docs & `libopus` macOS deployment target). **Incident 09** postmortem captures the pion/opus mode-3 → domain-boundary refactor.
- [x] **ADR-0017** Gateway–Engine topology: N:N-ready by design, realized by deployment
- [x] **ADR-0018** Language choice rationale — polyglot for portfolio, all-Go for product
- [x] **ADR-0019** RAG corpus + multilingual embedding pipeline (Python-seed impl mechanism **same-day superseded** by ADR-0020; six numbered decisions remain in force)
- [x] **ADR-0020** Engine owns inference — unified runtime for seed, query, ASR, future LLM; Python stays off-runtime tier
- [x] Taiwan zh-TW corpus bundled at `docs/rag/taiwan.md` (CC BY-SA 4.0, Wikipedia REST API extract)
- [x] README chief-of-staff positioning reframe — commit `e8f166e`
- [x] **Incident 08** (app_local fan-in channel drain) + **Incident 09** (pion/opus mode-3 refactor)

### Phase 3b: Engine inference implementation 🚧

> *"Make `engine --seed` produce real vectors; make the engine query against them."*

Driven by ADR-0020. Out-of-3b exit criterion: `engine --seed --corpus docs/rag/taiwan.md --target=local` writes a working Qdrant collection, and an in-engine query returns semantically relevant chunks for an English / Japanese / Chinese test query against the zh-TW corpus.

- [x] `engine_cpp/src/inference/embedder.h` — abstract `Embedder` interface (Embed(text) → vector) — Slice 1 (`0a9b4a7`)
- [x] **ADR-0021 shared ggml plumbing** — one ggml build consumed by both whisper.cpp and llama.cpp via `USE_SYSTEM_GGML` — Slice 3 (`7cd00e1`)
- [x] `engine_cpp/src/inference/ggml_embedder.{h,cc}` — default impl loading `BAAI/bge-m3` Q4_K_M GGUF via the shared ggml runtime through llama.cpp's C API — Slice 3 (`33f8eb7`)
- [x] bge-m3 Q4_K_M pinned in `models/manifest.json` with real SHA-256 (438 MB, `lm-kit/bge-m3-gguf`); fetched via `./tools/scripts/download_models.sh --model bge-m3-q4km` (manifest-driven, consistent with Phase 1 whisper-tiny pattern — ADR-0021 implementation note supersedes the earlier `third_party/bge_m3/` http_archive sketch, since runtime-loaded weights do not need Bazel build-time visibility) — Slice 4
- [x] C++ markdown chunker with the ADR-0019 §Decision.2 separator list (`\n\n`, `\n`, `。`, `！`, `？`, `，`, space); target chunk size ~450 chars, overlap ~80 — Slice 2 (`e1d23f0`)
- [x] `GGMLEmbedder` integration test with real bge-m3 weights — asserts dim == 1024, L2-normalized output, and related-pair cos-sim > unrelated-pair (English + Traditional Chinese) — Slice 4 (`engine_cpp/tests/integration/bge_m3_embed_test.cc`)
- [x] **ADR-0021 P3 CI version-match check** — `tools/scripts/check_ggml_versions.sh` (grep layer) + `bazel build //engine_cpp/tests/integration/...` in CI (link layer); two-layer design catches both "numbers diverged" and "same number, divergent source" drift per incident-10 — Slice 4
- [x] **ADR-0021 P4 upgrade SOP** — `CONTRIBUTING.md §Upgrading the ggml triple` documents the triple-bump procedure, PR convention (`deps(ggml-triple):`), and what to do when the drift check fails — Slice 4
- [x] **ggml triple bump** to v0.9.9 to unblock llama.cpp b8595's `gguf_*_ptr` symbols (incident-10 resolution) — Slice 4
- [x] Qdrant C++ client wired — Slice 5 (`dfadf5d`). Direct gRPC stubs generated from Qdrant v1.17.1 protos checked in at `proto/qdrant/v1.17.1/` (see `PROVENANCE.md` for the http_archive-vs-checked-in trade-off per incident-11). Surface is scoped to `CreateCollection` / `UpsertPoints` / `Search` — full API wrapper is YAGNI until a caller needs more.
- [x] `engine seed --corpus PATH --target={local|cloud}` subcommand in `engine_cpp/cmd/engine/` — Slice 6 (`a0e32be`). Subcommand dispatch (not flag-mode) per 2026-04-17 design decision; content-hash UUID-5-style point IDs via SHA-256; `aegis_<stem>` collection naming; payload = `{text, source_path, chunk_index}`; cloud target reads `QDRANT_URL` + `QDRANT_API_KEY` from env. Verified end-to-end against Taiwan corpus + local Qdrant v1.17.1: 10 chunks → `aegis_taiwan` collection, idempotent re-run. Multi-tenancy payload extension deferred to Phase 4 Cognito wiring (ADR-0022).
- [x] **Validation experiment (Slice 7)**: Taiwan corpus through `engine seed` into Qdrant, then scratch Python (`sentence-transformers` with `BAAI/bge-m3` FP reference, `qdrant-client` pulling the Q4_K_M vectors back out) compared per-chunk cosine similarity. **Result 2026-04-17**: N=10, **mean=0.9659**, median=0.9654, min=0.9591, max=0.9742 — all chunks individually above the 0.95 threshold, mean passes with ~1.6% margin. Phase 3b exit criterion met; no need to upgrade Q4_K_M → Q8_0 or revisit chunker params. Scratch script + `.venv` deleted post-validation per the scratch-Python-tool-tier discipline (ADR-0020 / `feedback_inference` memory).
- [x] **ADR-0010 revision**: split `ResourceBudget` into `ModelBudget` (process-global, ~500 MB for whisper + bge-m3 Q4_K_M) and `SessionBudget` (per-session, existing `kDefaultReservationBytes`). ADR updated in Slice 1 (`e61b192`); code landed in Slice 3.
- [x] **whisper.cpp deployment-target fix** (incident-09 Prevention follow-up): mirror what libopus did in commit `51835b1` — add `CMAKE_OSX_DEPLOYMENT_TARGET=11.0` to `whisper_cpp.BUILD` cache_entries, silencing the ~18 libggml-cpu warnings on the engine link — Slice 1 (`0d2bdb8`).

### Phase 3c: Frontend Host UI + cross-WebView acceptance ✅

> *"Make the chief-of-staff actually able to run a meeting, see hints, and export the transcript."*

Gated by 3b — prompter display needs real transcript data; corpus selector needs a real vector collection; consent + export flows need a real artifact to produce. **All six slices landed 2026-04-18** (PRs #19–#24); Playwright live-browser gate adds chromium + webkit to the CI matrix (Incident-09 lesson in code).

#### Frontend scaffolding
- [x] `FileSystemProvider`, `NotificationProvider`, `AutoUpdateProvider` scaffolding — Slice 1 (`#19`). `AuthProvider` pre-existed from Phase 2.
- [x] Respect all ADR-0002 Phase 3 Constraints 1–6 (no `chrome.*`, no Service Worker dependency, etc.) — `tools/scripts/check_frontend_tauri_compliance.sh` grep gate extended per slice

#### Host UI (Staff)
- [x] Login flow (Cognito Cloud / dummy Local) — pre-existed from Phase 2
- [x] "New Meeting" flow: RAG corpus selector → `CreateMeeting` RPC → session token display — Slice 2 (`#20`); RAG binding opt-in per ADR-0023 Decision B (empty `rag_id` first-class "no corpus" mode)
- [x] One-time audio-processing consent capture on first use (ARCH §9.3; no biometric consent needed — see ADR-0012) — Slice 3 (`#21`) — ADR-0024 Decision A
- [x] **Transcript consent — two-phase flow**: meeting-start toggle (default off; GDPR notice citing Art. 6(1)(f) + Art. 9(2)(a) on toggle-on; gates the transcript panel UI mode) + export-time confirmation modal (responsibility transfer + audit log with user id + timestamp + session id). **DO NOT** apply `user-select: none` to transcript text — screenshots bypass it and it breaks screen readers. — Slice 3 (`#21`) — ADR-0024 Decisions B, C, D; watermarking (Decision E) deferred to Phase 4+
- [x] Speaker label tagging UI — curated choice list, **no free-text name input** (ARCH §9.2) — Slice 4 (`#22`); `CURATED_SPEAKER_LABELS` closed set + reducer defense-in-depth rejects non-curated values
- [x] Live prompter display with rolling 5-line window (matches Viewer UI PROMPTER_WINDOW=5) — Slice 5 (`#23`); full transcript accumulates per ARCH §9.1, tail slice at render time only
- [x] Export flow: Markdown + JSON download — triggers transcript-consent phase-2 modal above — Slice 5 (`#23`); `lib/transcriptExport.ts` pure formatters resolve speaker overrides at export time
- [x] "End Meeting" button — pre-existed from Phase 2
- [x] QR code display for LAN viewer join (Local mode only) (ADR-0007) — pre-existed from Phase 2

#### Cross-WebView acceptance
- [x] Chrome / Edge primary testing — Playwright chromium project, Slice 6 (`#24`)
- [x] WKWebView sanity check on macOS (ensures Phase 4 Tauri wrap will not be blocked) — Playwright webkit project, Slice 6 (`#24`)
- [x] Firefox / Safari explicitly NOT supported for host role (L6); document in README — carried in existing README §Status / Known Gaps narrative
- [x] **Live-browser WebRTC smoke test harness** (incident-09 Prevention item): automate a real-browser (Playwright or Puppeteer) host → gateway → engine → transcript pipeline. Catches pion/opus-class "works on loopback, breaks on real browsers" regressions in CI rather than in production. — Slice 6 (`#24`); scope today is the consent-flow gate (6 tests, chromium + webkit); full audio-path smoke lands once Phase 4 Tauri makes the host a proper app process with predictable audio permissions.

---

## Phase 4: SRE & Cloud Orchestration

> *"Make it deployable. Sign it. Roll it out safely."*

### Phase 4a: Package
- [~] Bazel `rules_oci`: package C++ engine, Go GW, and frontend into Distroless OCI images
  - [x] Slice 1 — `rules_oci` + `rules_pkg` wired; Go gateway `aegis-gateway` image local-buildable, distroless `static-debian12:nonroot` base pinned by digest (ADR-0025)
  - [x] Slice 2 — SBOM generation (CycloneDX via `anchore/sbom-action` SHA-pinned syft v0.24.0) — gateway image SBOM emitted as workflow artifact `gateway-sbom-cyclonedx`; Phase 4b will sign as Cosign attestation
  - [x] Slice 3 — GitHub Actions ECR push via `github-actions-aegis-core-ecr` OIDC role: dedicated `release-staging-image.yml` workflow (push to `main` only), `oci_push` Bazel target, defense-in-depth re-smoke before push, tag scheme `staging-<git_sha>`. ldz #79 confirmed posture + queued `job_workflow_ref` IAM-trust-condition tightening on their side.
  - [x] Slice 4 — C++ engine `aegis-engine` image. Distroless `static-debian12:nonroot` tried first (per ADR-0025 §"Slice 4 distroless variant decision"); image is built by CI on Linux runners (Camp B trust — no defensive `target_compatible_with` against Mac builds, which Camp B forbids anyway). Models NOT shipped in image — runtime expects `/models` mounted from storage that ldz provisions per cross-repo issue [aegis-aws-landing-zone#82](https://github.com/BinHsu/aegis-aws-landing-zone/issues/82) (ldz picks EBS PV / S3+CSI / EFS based on their AWS-side trade-offs). Engine SBOM intentionally deferred to a follow-up mini-slice once distroless variant proves stable.
  - [ ] Slice 5 — Frontend `aegis-frontend` image (static asset packaging) + frontend-image SBOM
- [~] Each image runs as non-root with dropped capabilities and read-only root filesystem except tmpfs mounts
  - [x] Gateway image: `user = "nonroot"` (uid 65532), entrypoint is binary path (no shell), distroless ships no package manager (Slice 1)
- [~] Image tagging convention: `prod-<semver>-<git_sha>`, `staging-<git_sha>`, `dev-<git_sha>` — `staging-<git_sha>` live as of Slice 3; `prod-<semver>-<git_sha>` lands with prod-cut (Phase 4c+, ldz #79 Q1 deferred); `dev-<git_sha>` reserved for ad-hoc pushes
- [~] Produce SBOMs (Syft / CycloneDX) alongside every image (ARCH §10.1) — gateway image done in Slice 2; engine + frontend land with their respective image slices (4 / 5)

### Phase 4b: Sign & Scan
- [ ] Cosign / Sigstore signing in GitHub Actions using OIDC (ARCH §10.1)
- [ ] SLSA Level 3 provenance emission
- [ ] Trivy container scan; block push on critical CVEs
- [ ] kube-score + kube-bench manifest scan
- [ ] Checkov IaC scanner for K8s manifests + Dockerfile + Helm charts (complements kube-score/kube-bench from the misconfiguration / policy-as-code angle; see debrief discussion 2026-04-12)
- [ ] CodeQL, Semgrep, gosec, govulncheck, clang-tidy in CI (ARCH §10.2)
- [ ] Verify no binary contains `AEGIS_DEV_AUDIO_DUMP` symbol (ADR-0005 R7)
- [ ] ECR push pipeline; ArgoCD in `aegis-aws-landing-zone` repository polls the manifests in this repository

### Phase 4c: Progressive Delivery
- [ ] Argo Rollouts or Flagger integration in EKS manifests
- [ ] SLO-based canary gates (ARCH §10.4)
- [ ] Automatic rollback on error budget burn >25%
- [ ] Graceful shutdown verified end-to-end under rolling update (ADR-0006)
- [ ] Audio-namespace Kyverno / Gatekeeper policies (ADR-0005 R6): reject PVC, reject hostPath
- [ ] Velero backup schedule explicitly excludes `aegis-audio` namespace (ADR-0005 R6)
- [ ] **Engine startup manifest validation** — engine pre-flight walks `models/manifest.json`, verifies each `"required": true` model is present at the expected path with the expected SHA256, fails loudly with operator-readable error BEFORE starting the gRPC server. Today (Slice 4a-4) engine just `mmap`s the file and crashes on miss; honest enough for staging smoke, not acceptable for prod-cut. Surfaced in `packaging/engine/BUILD.bazel` "HONEST GAP" comment to keep the pre-flight visible until the work lands.
- [ ] **Post-deploy E2E suite against staging** — Playwright / API-level happy-path drive (CreateMeeting → audio → transcript → JoinAsViewer → EndMeeting) executed against the deployed staging URL after every ArgoCD sync. Today (Phase 4a) the only post-deploy verification is the kubelet `/healthz` probe (extremely narrow) and a human walking through the UI. Closing this gap converts "human notices the demo broke" into "machine blocks the next promotion."
- [ ] **Synthetic monitoring against staging + prod** — external probe (CloudWatch Synthetics, Better Uptime, or equivalent) hits public endpoints every N minutes and pages oncall on SLO-burn. Pairs with the canary gate above so a regression that escapes canary still gets caught within minutes.

### Phase 4d: Observability Wiring + Build Cache Decision

- [x] Pick a Bazel remote cache strategy per ADR-0014. Two-phase plan decided 2026-04-17: Option β (BuildBuddy Personal free tier) for the demo horizon, Option δ (S3 direct via Bazel 7.4+ `--credential_helper` + GHA OIDC → AWS IAM) for production. β wired into `.github/workflows/ci-baseline.yml`'s `bazel-unit-tests` job via a `Configure BuildBuddy remote cache` step that writes a runner-local `.bazelrc.user` when `BUILDBUDDY_API_KEY` secret is set (forks without the secret degrade gracefully to local execution). Runbook for the manual GHA-secret setup: [`docs/runbooks/buildbuddy-cache-setup.md`](docs/runbooks/buildbuddy-cache-setup.md). δ migration remains future work gated on `aegis-aws-landing-zone` publishing its AWS OIDC trust policy (ADR-0014 trigger T1) and will open a `cross-repo/blocking` issue per README §Cross-repo coordination ritual at that time.
- [ ] OTLP exporter to X-Ray / Tempo in Cloud, stdout in Local (ARCH §8)
- [ ] Custom `SpanProcessor` enforcing attribute allowlist (ADR-0005 R4)
- [ ] Structured JSON logs via FluentBit in Cloud
- [ ] Grafana dashboards and PagerDuty alerts provisioned by `aegis-aws-landing-zone` repository
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
