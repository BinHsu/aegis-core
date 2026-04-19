# ADR-0025: OCI image packaging via `rules_oci` + distroless static base

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-19                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4a — package the C++ engine, Go gateway, and frontend bundle into reproducible, signed-ready container images for EKS deployment. |
| Related  | ADR-0014 (Bazel build cache strategy), ADR-0015 (hermetic Node.js), CLAUDE.md Rule 6 (hermetic toolchains), `ARCHITECTURE.md` §10.1 (Supply Chain Integrity), ROADMAP §Phase 4a/4b |

## Context

Phase 3c closed with three buildable binaries (C++ `engine`, Go
`gateway`, frontend `dist/` bundle) all reachable through the hermetic
Bazel graph. Phase 4a's task is to package each of these into an OCI
image suitable for ECR push and EKS deployment, satisfying the
following non-negotiables drawn from `ARCHITECTURE.md` §10 and
`CLAUDE.md` Rule 6:

1. **Hermetic build.** The image-build action must not depend on a
   Docker daemon, host `docker build`, or any tool installed outside
   the Bazel graph. The "clone, build, it just works" promise extends
   to packaging.
2. **Reproducible.** Two builds of the same Git SHA must produce
   bit-identical image manifests (or at minimum: identical layer
   digests for all non-timestamped layers).
3. **Signable.** The output must be a standard OCI artifact that
   Cosign / Sigstore (Phase 4b) can sign without translation.
4. **Pinned base by digest, never tag.** Floating tags violate the
   dependency-pinning principle in `ARCHITECTURE.md` §10.1.
5. **Non-root, minimal attack surface.** Each image runs as a
   non-root user and ships only what the binary needs at runtime — no
   shell, no package manager, no debugging tools.

The decision space cleaved across two axes:

- **How** to produce the image: `Dockerfile` + `docker build`, `ko`
  (Go-specific), `Buildah`, `kaniko`, or `rules_oci`.
- **What** base image to use: `scratch`, distroless (Google), Alpine
  Linux, Wolfi (Chainguard), Ubuntu/Debian-slim.

## Decision

### Build tool — `rules_oci` v2 (bazel-contrib)

`rules_oci` is the Bazel-native rule set that constructs OCI images
purely from Bazel actions, with no Docker daemon dependency. It pulls
base images via the OCI distribution spec, layers Bazel-built artifacts
on top via `pkg_tar`, and emits standard OCI image directories that
Cosign / SBOM tooling consume directly.

### Base image — `gcr.io/distroless/static-debian12:nonroot`

Pinned by SHA256 manifest-list digest (multi-arch: linux/amd64 +
linux/arm64/v8). The `:nonroot` variant ships a uid/gid 65532 user so
no Dockerfile-equivalent `useradd` action is required.

### Image layout convention

- Binaries land at `/usr/local/bin/<binary-name>`.
- Entrypoint is the binary path, not a shell wrapper (no shell exists).
- Image runs as `user = "nonroot"`; rootfs is read-only-compatible
  (no writes outside `/tmp` if a tmpfs is mounted at runtime).
- OCI annotations include `org.opencontainers.image.source`, `.title`,
  `.description`, `.licenses`. Tag-level provenance (git SHA, semver
  per ROADMAP `:305` convention) attaches at push time (Slice 3).

### Sequencing across slices

| Slice | Scope | Image set |
| --- | --- | --- |
| 4a-1 (this ADR) | `rules_oci` wiring + Go gateway image; local-only, no push | `aegis-gateway` |
| 4a-2 | SBOM via Syft (CycloneDX) attached to each image | (no new image) |
| 4a-3 | GitHub Actions ECR push via OIDC role from ldz #74 | (no new image) |
| 4a-4 | C++ engine image — exercises hermetic clang × distroless | `aegis-engine` |
| 4a-5 | Frontend image — static asset packaging | `aegis-frontend` |
| 4b   | Cosign signing + SLSA L3 + Trivy scan | (gates the above) |

## Dev experience vs CI validation — Camp B / dev-CI split

The Docker-era industry split into two camps on the question of
"should developers run containers locally":

- **Camp A (dev/prod parity):** developers `docker compose up` to
  bring up the stack. Heroku-influenced, popular at typical SaaS
  scale-ups. Catches container-only bugs early; slows dev iteration;
  forces every contributor to install Docker.
- **Camp B (Bazel-native):** developers `bazel run` binaries
  directly. Container images are deployment artifacts validated by
  CI, not dev tools. Used at Google, Meta, and most Bazel/Buck
  monorepo shops. Preserves dev velocity; container parity enforced
  by CI smoke; **mandatory CI runtime test** is the missing-link cost.

aegis-core picks **Camp B**. Reasons:

1. **Architectural consistency.** ADR-0014 (hermetic remote cache),
   ADR-0015 (hermetic Node.js), CLAUDE.md Rule 6 (hermetic
   toolchains) already commit the project to Bazel-native
   reproducibility. Camp A would force every contributor to install
   Docker Desktop, breaking the "clone, build, it just works" promise.
2. **Dev velocity.** `bazel run //gateway_go/cmd/gateway:gateway`
   produces and executes a host-native binary in seconds; the same
   path through Docker would rebuild image layers on every code
   change.
3. **Defensibility.** Camp B's reasoning is the dominant view in the
   Bazel / SRE-handbook literature; it survives interview scrutiny.

### What runs where

| Action | Dev box | CI |
| --- | --- | --- |
| `bazel run //gateway_go/cmd/gateway:gateway` (host-native dev gateway) | ✅ direct exec, no Docker | n/a |
| `bazel build //packaging/gateway:image` (image-graph build) | ✅ no Docker needed | ✅ part of `bazel-unit-tests` job |
| `bazel run //packaging/gateway:image_load` + `docker run …` (image runtime smoke) | optional, requires local Docker | ✅ **mandatory CI step** — `--read-only` + `--user 65532:65532` + curl `/healthz` within 10s |

Concretely: Mac contributors **never have to install Docker** to develop, review, or merge changes. CI is the single source of truth for "image actually boots inside a container."

### Mandatory CI runtime smoke — the Camp B keystone

The CI smoke step in `.github/workflows/ci-baseline.yml`
(`bazel-unit-tests` job) loads the image into the runner's Docker
daemon, starts it with `--read-only` and `--user 65532:65532`, then
curls `/healthz` until 200 (10s deadline). This catches:

- Image structure errors (entrypoint path wrong, layer order broken).
- Static-linking gaps (binary references a libc symbol that distroless
  doesn't provide).
- Read-only rootfs violations (binary tries to write `/var/run`
  without a tmpfs mount).
- Nonroot user permission errors (binary tries to bind a port < 1024).

Without this step, Camp B's promise is hollow. Removing or weakening
this gate requires explicit ADR amendment.

## Promotion chain to staging

This ADR's Slice-1 wiring is the **leftmost gate** in a multi-stage
chain that ends with the image running in staging EKS. The full chain:

| # | Gate | Slice / Phase | What it proves |
| --- | --- | --- | --- |
| 1 | CI smoke (this ADR) | 4a-1 | image builds + boots + `/healthz` 200 + read-only rootfs honored |
| 2 | SBOM emission | 4a-2 | every layer has a CycloneDX SBOM (`ARCHITECTURE.md` §10.1) |
| 3 | ECR push via OIDC | 4a-3 | image lands in `251774439261.dkr.ecr.eu-central-1.amazonaws.com/aegis-core` |
| 4 | Trivy CVE scan | 4b | no critical CVE; otherwise push blocked |
| 5 | Cosign keyless sign | 4b | image signed via GitHub OIDC → Sigstore Fulcio |
| 6 | K8s manifest tag bump | 4a-3+ | `deployment.spec.containers.image` references the new digest |
| 7 | ArgoCD sync (ldz repo) | ldz GitOps phase | staging EKS picks up the manifest change |
| 8 | Kyverno verify-image admission | 4b | rejects pod if Cosign signature absent or invalid |
| 9 | kubelet startup probe | runtime | EKS itself runs the binary and re-checks `/healthz` |

Slice 1 implements Gate 1 only; subsequent slices and Phase 4b add
Gates 2–8; Gate 9 is enforced by Kubernetes itself once Phase 4c
deployment manifests land.

### Honest gap (today, demo horizon)

Between Gate 1 (CI smoke) and Gate 9 (kubelet probe) there is **no
automated end-to-end test that exercises the deployed staging stack**.
A regression that escapes CI but doesn't crash the binary on startup
(e.g. WebRTC handshake silently broken, transcript egress wired to the
wrong session) lands in staging and is caught only when a human walks
the demo. That is acceptable at the demo horizon (zero real traffic,
no SLO commitments) but is not acceptable for production. The closing
work is tracked in `ROADMAP.md` Phase 4c — "Post-deploy E2E suite
against staging" + "Synthetic monitoring against staging + prod" —
and is the prerequisite gate for any path that puts paying users on
the system.

## Cross-platform compilation — Option C

A related design question for Slice 1 was: how should the gateway
binary be cross-compiled to Linux/amd64 from Mac dev boxes when
building the image?

**Selected: explicit named cross-binary target.**

`gateway_go/cmd/gateway/BUILD.bazel` declares two targets:

- `//gateway_go/cmd/gateway:gateway` — `go_binary`, no `pure` /
  `static` attributes. Default for `bazel run`; produces a host-native
  binary (Mach-O arm64 on Apple Silicon, ELF amd64 on CI runners
  and prod). Used for everyday development. **`static = "on"` is
  intentionally omitted**: rules_go documents it as Linux-only and
  silently triggers a platform transition to linux_amd64 when set on
  macOS, which would turn `bazel run :gateway` into a broken
  Linux-only binary on Mac dev boxes. Pure-Go binaries with no cgo
  are self-contained on any OS without needing the attribute, and
  distroless `static-debian12:nonroot` runs them without libc. If a
  future dep introduces cgo, the cross-compile to linux_amd64 below
  fails loudly at link time on Mac (no Linux cross C-toolchain) —
  forcing the discussion before merge.
- `//gateway_go/cmd/gateway:gateway_linux_amd64` — `go_cross_binary`
  wrapping `:gateway` with a `linux_amd64` platform transition.
  Consumed by `//packaging/gateway:image` so the image is **always**
  a Linux artifact regardless of which host runs `bazel build`. Devs
  can also `bazel build` this target directly to inspect the Linux
  ELF without invoking Docker.

Rejected alternatives:

- **A. Implicit (no cross-binary):** image picks up host-platform
  binary. On Mac dev boxes that produces a darwin binary inside a
  Linux image — `docker run` fails with "exec format error". Hidden
  failure mode that violates "clone and run."
- **B. Force gateway to always be `goos = "linux"`:** breaks the dev
  workflow on Mac (no native dev binary).

Multi-arch (`gateway_linux_arm64` for Graviton EKS nodepools) and a
generic `--config=linux-amd64` `.bazelrc` recipe are deliberately
**not** added in Slice 1 — YAGNI. Each is a sub-10-line addition
when a concrete consumer materialises (Slice 3 image_index for arm64;
batch cross-target build flag for Slice 4 engine + Slice 5 frontend).

## Alternatives considered

### Build tool

- **Dockerfile + `docker build`** — rejected. Requires a Docker daemon
  on every build host (local + CI), violates the hermetic promise, and
  produces non-reproducible layers because of timestamp drift in
  `ADD` / `COPY` operations. Bazel's content-addressable model also
  becomes useless: each `docker build` is opaque to the Bazel cache.
- **`ko` (Google's Go-native image builder)** — rejected. Excellent
  for pure-Go monorepos, but only supports Go. We need a single
  packaging story that works for the C++ engine and the frontend
  bundle too.
- **Buildah / kaniko** — rejected. Both eliminate the Docker daemon
  dependency, but neither integrates with the Bazel action graph. The
  caching, hermeticity, and reproducibility properties we get from
  `rules_oci` would have to be reimplemented around the tool.

### Base image

- **`scratch`** — rejected. Zero overhead, but ships no
  `/etc/passwd`, no CA certificates, no `/tmp`. Adding these manually
  defeats the simplicity argument; using distroless `static` gets us
  the same posture with the awkward bits already solved upstream.
- **Alpine Linux** — rejected. musl libc creates DNS-resolution edge
  cases (notoriously, Go's `net` package against Alpine's musl) and
  ships a shell, package manager, and BusyBox utilities that broaden
  the attack surface for no production benefit.
- **Wolfi (Chainguard)** — strong runner-up. Daily-rebuilt, signed,
  glibc-based, SBOM-attached upstream. The reasons we did **not**
  pick it for the foundation: (a) the Chainguard public images are a
  marketing surface for the paid product and could change cadence /
  registry path with limited notice, and (b) distroless has been the
  Bazel ecosystem's canonical answer for nearly a decade — every
  `rules_oci` example targets it, every troubleshooting answer
  assumes it. Reserving Wolfi as a documented re-evaluation when
  Phase 4b SBOM workflows expose any pain points with the distroless
  upstream.
- **Ubuntu / Debian-slim** — rejected. Order-of-magnitude larger
  attack surface, ships `apt`, ships a shell. No production
  justification for a Go binary that needs only static linking.

## Consequences

### Positive

- Container packaging inherits Bazel's reproducibility + caching
  properties end-to-end.
- BuildBuddy remote cache (ADR-0014) covers image layers
  automatically; CI cold-build for new images amortises across PRs.
- Cosign / SLSA / SBOM tooling in Phase 4b plug into a standard OCI
  artifact, no glue code.
- Multi-arch (amd64 + arm64) is one extra `platforms = […]` entry,
  not a separate pipeline.

### Negative

- `rules_oci` v2 is younger than the Aspect / bazel-contrib baseline
  has been overall; API drift across minor versions is possible.
  Mitigation: pin to `2.0.1` in `MODULE.bazel`; bumps are explicit
  PRs with CI smoke-testing the change.
- Distroless's update cadence is upstream-controlled; we accept their
  release rhythm. When a CVE-relevant base bump lands, it's a one-line
  digest change in `MODULE.bazel`.
- Distroless `static` requires fully static binaries (no CGO). For the
  Go gateway this is satisfied via `pure = "on"` + `static = "on"` in
  the `go_binary` rule. The C++ engine (Slice 4) will need a
  different distroless variant (`base-debian12` or `cc-debian12`) —
  documented as an open question to revisit when Slice 4 starts.

### Out of scope (this ADR)

- Image tagging conventions for ECR (Slice 3).
- SBOM attestation format, location, and lifecycle (Slice 2).
- Cosign keyless signing key management, Fulcio trust roots, and
  admission-time verification (Phase 4b + landing-zone Kyverno
  policy).
