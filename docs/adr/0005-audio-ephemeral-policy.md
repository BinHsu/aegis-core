# ADR-0005: Audio Ephemeral Policy

- **Status**: Accepted
- **Date**: 2026-04-11 (original)
- **Last revised**: 2026-04-11 (removed voiceprint scope per ADR-0012)
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —
- **Revision history**: Originally titled "Audio and Voiceprint Ephemeral
  Policy". The voiceprint sub-policy was removed when Aegis dropped
  voiceprint matching per ADR-0012; the seven enforcement requirements
  (R1–R7) remain in full force for audio PCM.

## Context

Aegis Core processes raw audio PCM that carries the highest privacy and
regulatory risk in the system's data plane. Unlike transcript text,
which is already filtered through speech recognition and covered by the
statelessness guarantee of ADR-0004, audio PCM contains the original
acoustic signal — tone, pacing, identifiable voice characteristics, and
the complete conversational content.

A policy statement like *"we don't store audio"* is insufficient on its
own. Core dumps, swap partitions, trace logs, backup systems, debug
dumps, and accidental TRACE-level logging can all defeat naive
ephemeral claims. A trustworthy ephemeral policy must be **mechanically
enforced and testable**, not a matter of developer discipline.

This ADR establishes the **ephemeral policy** for audio PCM and the
**seven enforcement requirements** that turn "we don't store audio"
from marketing language into a CI-verifiable engineering property.

**Note on scope change (2026-04-11)**: this ADR originally also
covered voiceprint embeddings. Per ADR-0012, Aegis no longer performs
voiceprint matching; biometric data is not processed by the system at
all. The voiceprint sub-policy has been removed from this document.
The R1–R7 enforcement requirements are unchanged and remain mandatory
for audio PCM.

## Decision Drivers

- **D1. Audio content sensitivity.** Even transient audio that leaks
  via a core dump, swap partition, or debug log is a content-disclosure
  incident. The bar for "we don't store audio" must be high enough to
  survive adversarial scrutiny.
- **D2. Enforceability over intention.** A policy that relies on
  developer discipline will eventually be violated; a policy that
  relies on compile-time or deployment-time enforcement will not.
- **D3. Testability.** The policy must be verifiable by automated
  checks (CI, deployment gates, runtime assertions), not just by code
  review.
- **D4. Operational acceptability.** Debugging, crash investigation,
  and performance profiling must remain possible without weakening
  the ephemeral guarantee.

## Scope

This ADR covers:

- Audio PCM buffers throughout the Go Gateway and C++ engine processes.
- Any derived artifact of audio that could reconstruct the original
  signal in whole or in part (spectrograms, intermediate `whisper.cpp`
  tensors, speaker diarization internal state, audio ring buffers,
  resampling scratch space).

This ADR **does not** cover:

- Transcript text (handled by ADR-0004 — held only in fan-out buffers,
  no server-side persistence, and not biometric in the regulatory
  sense).
- Anonymous speaker diarization labels (`Speaker_0`, `Speaker_1`, …) —
  these are pseudonymous session-local identifiers, not PII, and are
  blessed by GDPR Art. 25 as privacy-by-design. See
  `ARCHITECTURE.md` §9.2.
- RAG knowledge base content (user-managed persistent corpus,
  orthogonal to meeting data).
- **Voiceprint / biometric data** — Aegis does not process voiceprint
  data at all. See ADR-0012.

## Policy

### Audio PCM

- **Lifetime**: from `RTCPeerConnection` ingest to the last
  `whisper.cpp` inference call that consumes it. Discarded immediately
  after.
- **Location**: C++ engine process heap only. Never serialized to
  disk, never copied to another process, never sent over any network
  except the already-authenticated gRPC stream from the Go Gateway to
  the C++ engine.
- **Session scope**: when the session ends (host disconnect, user ends
  meeting, engine terminates), all audio buffers are freed **before**
  session cleanup returns.

### Exclusions

- Aegis does **not** train models on user audio. `whisper.cpp`,
  diarization, and any optional generative models are pretrained and
  used only for inference.
- Aegis does **not** sell, share, or otherwise disclose audio to any
  third party.
- Aegis does **not** retain audio for product improvement, analytics,
  QA, debugging, or any other purpose.
- Aegis does **not** process voiceprint / biometric data. See
  ADR-0012. This exclusion is structural — the engine does not
  contain an embedder, a cosine matcher, or any per-session voiceprint
  vault.

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
- Enforcement target:
  `//engine_cpp/tests/unit:no_dev_audio_dump_symbol_test` (sh_test).
  Greps the linked engine binary for `AEGIS_DEV_AUDIO_DUMP`. Default
  `bazel test` runs in fastbuild (no define set) so the string is
  absent and the test passes. If a `-DAEGIS_DEV_AUDIO_DUMP` copt
  leaks into any non-debug build path, the `#ifdef`-guarded banner
  string at `engine_cpp/cmd/engine/main.cc:138-145` lands in
  `.rodata` and the grep matches. `--strip=always` on release
  (`.bazelrc:82`) removes symbol tables but NOT `.rodata`, so the
  string survives stripping and the gate catches it. Included in
  the PR CI matrix via `ci-baseline.yml` → `bazel test`.
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

- Strong, mechanically-enforced ephemeral guarantee for audio PCM.
- Ephemeral claims become testable in CI — they are engineering
  properties, not marketing claims.
- Debugging remains possible via golden audio fixtures, WER regression
  tests, and synthetic audio (per R7 rationale and ADR-0011).
- Clean compliance posture: "we do not retain audio" is a checkable
  engineering property, not a policy promise.

### Negative

- **Cannot debug with real production audio.** If a customer reports a
  transcription bug, we can only reproduce it via (a) their
  description, (b) a synthetic audio with matching characteristics, or
  (c) a customer-provided sample they explicitly opt in to share for
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
- ADR-0010 C++ Engine Runtime Architecture (`SensitiveBytes` usage in
  R3)
- ADR-0012 Remove Voiceprint Matching (scope change rationale)
- `ARCHITECTURE.md` §6 AI Models & Hardware Resource Optimization
- `ARCHITECTURE.md` §9 Data Governance & Privacy
- `ARCHITECTURE.md` §11 Known Limitations
- `docs/threat-model.md`
