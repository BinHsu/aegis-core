# ADR-0005: Audio and Voiceprint Ephemeral Policy

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core processes two categories of data that carry the highest privacy
and regulatory risk in the entire system:

1. **Raw audio PCM** — the captured voice of the host staff, the boss,
   and any counterparties. Contains speech content (often business-
   sensitive or personal), voice tone, and identifiable biometric
   characteristics.
2. **Voiceprint embeddings** — numerical vectors (typically 192 or 256
   dimensional `float32` arrays) derived from voice samples, used to
   identify specific speakers within a meeting's diarization output
   ("this segment was spoken by the person who enrolled as 'The Boss'").

Voiceprint embeddings are **biometric data** under GDPR Art. 4(14),
BIPA (Illinois 740 ILCS 14), Texas CUBI, and CCPA's "sensitive personal
information" category. Mishandling them creates both regulatory exposure
(potential class actions with nine-figure precedents) and severe
reputational risk.

This ADR establishes the **ephemeral policy** for both categories and
the **seven enforcement requirements** that turn "we don't store audio"
from marketing into an engineering property.

Mere statements of intent ("we don't save it") are insufficient. Core
dumps, swap partitions, trace logs, backup systems, debug dumps, and
accidental TRACE-level logging can all defeat ephemeral claims. A
trustworthy ephemeral policy must be mechanically enforced and testable.

## Decision Drivers

- **D1. Biometric data compliance.** Voiceprint embeddings fall under
  GDPR Art. 9 special-category data and BIPA biometric identifiers.
  Persistence dramatically raises the regulatory bar (explicit opt-in
  consent, data protection impact assessment, heightened breach
  notification, destruction schedules, private right of action in
  Illinois).
- **D2. Audio content sensitivity.** Even transient audio that leaks
  via a core dump, swap partition, or debug log is a content-disclosure
  incident. The bar for "we don't store audio" must be high enough to
  survive adversarial scrutiny.
- **D3. Enforceability over intention.** A policy that relies on
  developer discipline will eventually be violated; a policy that
  relies on compile-time or deployment-time enforcement will not.
- **D4. Testability.** The policy must be verifiable by automated
  checks (CI, deployment gates, runtime assertions), not just by code
  review.
- **D5. Operational acceptability.** Debugging, crash investigation,
  and performance profiling must remain possible without weakening
  the ephemeral guarantee.

## Scope

This ADR covers:

- Audio PCM buffers throughout the Go Gateway and C++ engine processes.
- Voiceprint embedding vectors within the C++ engine.
- Any derived artifact of either (e.g., spectrograms, intermediate
  whisper.cpp tensors, speaker diarization internal state).

This ADR **does not** cover:

- Transcript text (handled by ADR-0004 — held only in fan-out buffer,
  no server-side persistence, but not "biometric" in the regulatory
  sense).
- Consent ledger entries (separate store, covered in
  `ARCHITECTURE.md` §9 Data Governance & Privacy).
- RAG knowledge base content (user-managed persistent corpus,
  orthogonal to meeting data).

## Policy

### Audio PCM

- **Lifetime**: from `RTCPeerConnection` ingest to the last whisper.cpp
  inference call that consumes it. Discarded immediately after.
- **Location**: C++ engine process heap only. Never serialized to
  disk, never copied to another process, never sent over any network
  except the already-authenticated gRPC stream from Go Gateway to
  C++ engine.
- **Session scope**: when the session ends (host disconnect, user
  ends meeting, engine terminates), all audio buffers are freed before
  session cleanup returns.

### Voiceprint Embeddings

- **Creation**: generated during per-session enrollment (the user says
  "test123" or an equivalent short phrase) at the start of each meeting.
- **Lifetime**: session-scoped, C++ engine process heap only.
- **Re-enrollment**: every new meeting requires a new enrollment.
  There is **no persistent voiceprint store** in MVP — no cross-
  meeting "remember this speaker" feature.
- **Consent capture**: at enrollment, the UI captures explicit user
  consent with a clear privacy notice. The consent **record** (user ID,
  timestamp, consent version, client metadata) is persisted to a
  **separate consent ledger** (covered in `ARCHITECTURE.md` §9). The
  voiceprint embedding itself is not persisted — only the record that
  consent was given.
- **Deletion**: automatic when the C++ engine process terminates or
  the session ends, whichever comes first.

### Exclusions

- Aegis does **not** train models on user audio or voiceprints. The
  whisper.cpp, diarization, and embedding models are pretrained and
  used only for inference.
- Aegis does **not** sell, share, or otherwise disclose audio or
  voiceprint data to third parties.
- Aegis does **not** retain audio or voiceprints for product
  improvement, analytics, QA, debugging, or any other purpose.

## The Seven Enforcement Requirements

These are mechanical, testable requirements that turn the policy into
an engineering property. Each is mandatory for any environment that
handles production audio.

### R1. Core Dumps Disabled on Audio-Processing Processes

A C++ segfault or abort could write a core dump containing the current
PCM heap. This must not happen on production pods / processes.

**Enforcement**:

- Pod `securityContext` sets `sysctls: [{name: kernel.core_pattern,
  value: "|/bin/false"}]` or equivalent, OR container entrypoint
  invokes `ulimit -c 0` before execing the engine.
- Local mode: the `bazel run //:app_local` launcher sets
  `RLIMIT_CORE=0` on the engine child process via `exec.Command` in
  the Go supervisor.
- Production images built with `CMAKE_BUILD_TYPE=MinSizeRel` and
  `-fno-omit-frame-pointer` to allow symbolic backtraces without
  needing a core file.
- For crash investigation, use a minimal **stack-only** crash
  reporter (e.g., Breakpad configured with heap dumping disabled).

**Verification**: CI smoke test attempts to trigger a segfault in a
canary build and asserts no core file is written to any filesystem
path.

### R2. Swap Disabled on Audio-Processing Nodes

If the kernel pages out an audio buffer to swap, the "ephemeral" claim
becomes a lie — the bytes survive on disk until overwritten.

**Enforcement**:

- Kubernetes node pool for audio-processing pods has
  `kubelet --fail-swap-on=true` (the cluster default in modern EKS, but
  must be verified).
- Node pool label / taint: `aegis.io/audio-node=true` +
  `NoSchedule` taint for non-audio workloads.
- Audio pods tolerate the taint and have nodeSelector matching the
  label.
- Local mode: the launcher calls `mlockall(MCL_CURRENT | MCL_FUTURE)`
  via Rust / Go syscall on macOS and Linux where permitted, falling
  back to a warning on permission failure.

**Verification**: a CI integration test checks `cat /proc/swaps` on
the audio node and asserts it is empty.

### R3. Log Formatter Type Whitelist

The single most common way ephemeral claims are violated: a developer
writes `log.Debugf("buffer: %v", audioBuf)` and the audio bytes flow
into CloudWatch. This must be structurally prevented.

**Enforcement**:

- **C++**: audio buffers are typed as `class SensitiveBytes` with
  `operator<<` explicitly deleted and a logger-safe overload returning
  `"[REDACTED Nbytes]"`. Any attempt to log a `SensitiveBytes` without
  the safe overload is a compile error.
- **Go**: audio buffers are typed as `type RedactedPCM []byte` with a
  `String() string` / `Format(fmt.State, rune)` method that returns
  `"[REDACTED Nbytes]"`. Never accessed via `%v` or `%+v`.
- **CI guard**: a Semgrep rule forbids `fmt.Sprintf` / `fmt.Printf`
  patterns that interpolate variables of type `RedactedPCM` without
  using the safe formatter.
- **TypeScript** (for completeness, though audio in browser is also
  ephemeral): any `MediaStream` or `ArrayBuffer` containing PCM must
  not be passed to `console.*` or to any structured logger.

**Verification**: Semgrep / clang-tidy rules run in CI; violations
block merge.

### R4. OpenTelemetry Span Attribute Audit

Distributed tracing spans can carry attributes — and developers may
innocently add transcript text or audio metadata. Span attributes get
exported to an observability backend and can end up in long-term
storage.

**Enforcement**:

- A custom `SpanProcessor` inspects every span before export and
  rejects (or scrubs) attributes whose values exceed a size threshold
  (e.g., 256 bytes) or match audio / transcript patterns.
- Span attribute **allowlist**: only `request_id`, `session_id`,
  `tenant_id`, `speaker_count`, `duration_ms`, `sample_rate`,
  `rag_id`, `error_code`, and explicitly approved others may be
  attached. Anything not on the allowlist is dropped at the span
  processor.

**Verification**: integration test attempts to attach a forbidden
attribute and asserts the emitted span does not carry it.

### R5. Temp Files on Memory-Backed Filesystems Only

If `whisper.cpp` or any component writes a temporary file (resampling
buffer, intermediate tensor, lock file), it must land on a memory-
backed filesystem (`tmpfs`), not host disk.

**Enforcement**:

- Kubernetes: audio pods mount `emptyDir` volumes with
  `medium: Memory`. Any directory the engine might write to
  (`/tmp`, `/var/tmp`, app-specific temp) is backed by such a volume.
- Local mode: `TMPDIR` environment variable set to a process-private
  directory that, on supported platforms, is a tmpfs (`/tmp` on
  macOS is not tmpfs by default but is ephemeral across reboots; on
  Linux, `/dev/shm` or a user-owned tmpfs mount is used when
  available).
- `whisper.cpp` is audited for file writes; any unavoidable file
  writes are redirected to the managed temp directory via a
  runtime constant.

**Verification**: integration test inspects `open(2)` syscalls during
a canary inference via `strace` / `dtruss` and asserts no writes occur
to non-tmpfs paths.

### R6. No Persistent Volume Mounts on Audio Namespaces

A stray `PersistentVolumeClaim` on an audio-processing pod, or a
Velero backup job that snapshots an audio pod's volumes, can leak
in-flight buffers onto durable storage.

**Enforcement**:

- Kyverno / OPA Gatekeeper policy: in the `aegis-audio` namespace,
  pods with `persistentVolumeClaim` volumes are **rejected** at
  admission. `hostPath` volumes are also rejected.
- Velero backup schedule **excludes** the `aegis-audio` namespace.
- Network policy: audio-processing pods deny egress to any
  CIDR / service except the Go Gateway service, preventing data
  exfiltration via unexpected channels.

**Verification**: Gatekeeper policy test manifests; CI applies a
violating manifest and asserts admission rejection.

### R7. Debug Audio Dump Compiled Out of Production Builds

Developers will eventually want to dump a "problematic" audio sample
for offline analysis. This capability must **not** exist in production
builds under any runtime flag — a runtime flag is too easy to turn on
by accident or malice.

**Enforcement**:

- C++ debug audio dump code is gated by `#ifdef AEGIS_DEV_AUDIO_DUMP`,
  which is defined **only** when `-c dbg` is passed to Bazel. Release
  builds (`-c opt`) strip the code at compile time; it is not present
  in the binary.
- Go debug audio dump code is gated by a build tag
  `//go:build aegis_dev_audio_dump`. Release builds do not include the
  tag.
- Container images tagged `prod-*` are built with release flags and
  verified via a CI check: `grep "AEGIS_DEV_AUDIO_DUMP" on the binary`
  must return empty for prod images.
- CI gate: any PR that introduces a runtime toggle (environment
  variable, CLI flag, config file key) for audio dumping is flagged
  for human security review.

**Verification**: automated CI check on release artifact binaries.

## Decision Outcome

**We adopt the above policy and the seven enforcement requirements as
mandatory for any environment handling production audio.**

All seven requirements apply to:

- Cloud mode (`DeployMode=CLOUD`).
- Local mode (`DeployMode=LOCAL`) with best-effort adaptation where
  Kubernetes-specific mechanisms (R2 kubelet, R6 Gatekeeper) are not
  applicable. Local mode must still enforce R1, R3, R4, R5, R7 via
  equivalent mechanisms (process-level `ulimit`, type system, span
  processor, `TMPDIR`, compile-time stripping).

## Consequences

### Positive

- Strong, mechanically-enforced ephemeral guarantee for audio and
  voiceprints.
- Compliance story for biometric data becomes defensible: RAM-only,
  session-scoped, consent captured, non-trainable, non-resold.
- Debugging remains possible via golden audio fixtures, WER
  regression tests, and synthetic audio (R7 rationale).
- Ephemeral claims become testable in CI — they are engineering
  properties, not marketing claims.

### Negative

- **Cannot debug with real production audio.** If a customer reports a
  transcription bug, we can only reproduce it via (a) their
  description, (b) a synthetic audio with matching characteristics, or
  (c) a customer-provided sample they explicitly opt-in to share for
  debugging. This is an accepted trade-off.
- **Crash investigation is stack-only.** No heap snapshots. Complex
  memory corruption bugs are harder to diagnose.
- **R6 (no PVC in audio namespace)** precludes some K8s patterns like
  shared-nothing caches mounted as PV.
- **Requires discipline at code-review time** in addition to the
  automated checks, since any new log / span / temp-file site is a
  potential violation.
- **Local mode enforcement is weaker** than cloud mode on R2 and R6
  because user laptops are not under our operational control. This is
  documented and accepted as a difference in threat model (local-mode
  users are trusting their own hardware).

## Related

- ADR-0003 Host Audio Capture Strategy
- ADR-0004 Stateless Broadcast Relay
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §9 Data Governance & Privacy
- `ARCHITECTURE.md` §11 Known Limitations
- `docs/threat-model.md`
