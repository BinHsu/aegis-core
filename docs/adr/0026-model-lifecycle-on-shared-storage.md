# ADR-0026: Model lifecycle on shared persistent storage (multi-version coexistence)

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-19                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 4a Slice 4 ships the engine OCI image; Phase 4c will ship K8s manifests that mount model storage. Rolling deployments will run multiple engine pod versions concurrently, each potentially needing a different model version. The storage shape and the engine's loading contract must be designed together to avoid version coordination becoming an incident class. |
| Related  | ADR-0021 (shared ggml runtime), ADR-0025 (OCI packaging strategy), `aegis-aws-landing-zone#82` (model storage requirement spec), `models/manifest.json` schema |

## Context

Slice 4 deliberately keeps models out of the engine OCI image (~50-100 MB image vs ~1.5 GB if baked in; ADR-0025 §"Slice 4 distroless variant decision"). Engine reads `/models` at runtime from a Kubernetes-mounted directory backed by ldz-provisioned storage (EBS PV, S3+CSI, or EFS — ldz #82).

The follow-on architectural question, surfaced during Slice 4 review: **what happens during a rolling deployment** when:

- Old engine pods (still serving traffic, terminating per `terminationGracePeriodSeconds`) need model **version V1**;
- New engine pods rolling out need model **version V2**;
- Both pods mount the **same `/models` directory**.

If V1's file is replaced in-place by V2 to "upgrade", old pods experience the file disappearing under their `mmap` — undefined behavior, likely SIGBUS. If both files coexist, the engine must know which version it requires.

Three approaches were considered:

| Approach | Mechanism | Storage shape | Engine responsibility |
| --- | --- | --- | --- |
| A — Per-pod fetch | Pod startup downloads the right version from S3 to ephemeral local | None (or ephemeral) | Heavy startup logic |
| B — Versioned subdirs | `/models/v1/...`, `/models/v2/...`; env var `AEGIS_MODEL_VERSION` picks subdir | Subdirs per version | Read env var, look in subdir |
| C — Filename-as-version, manifest-driven | `/models/whisper-large-v3-turbo.q4.gguf`, `whisper-large-v3-turbo.q5.gguf`; engine reads manifest, picks filenames | Flat dir, all active versions | Walk bundled manifest, pick required filenames, SHA-verify |

## Decision

**Approach C: filename-as-version, manifest-driven engine + flat-directory storage.**

### The contract — split between engine and storage

#### Engine responsibilities

1. **Bundle the manifest.** The engine OCI image embeds (compiled in OR copied as a layer) `models/manifest.json` for the version it was built against. The manifest encodes, for each model the engine requires, the canonical filename + expected SHA256.

2. **Walk the manifest at startup, before serving traffic.** For each model entry where `"required": true`:
   - Resolve `<storage_root>/<filename>` (storage_root is `/models` by default, overridable via `AEGIS_MODEL_PATH` env var per `engine_cpp/cmd/engine/main.cc:68-73`).
   - Verify the file exists; fail with operator-readable error if not.
   - Verify the SHA256 of the file matches the manifest entry; fail with operator-readable error if not.
   - Only after all required models pass, start the gRPC server.

3. **Never write to `/models`.** The mount is read-only-friendly (and the K8s manifest will declare it `readOnly: true`). Engine binary has zero code paths that mutate the directory.

4. **Tolerate "extra" files.** Other versions / future additions / older models still pinned by other concurrently-running pods MUST not cause this engine to refuse to start. The walk is "do my required entries exist with correct SHA?", not "is the directory exactly what I expect?".

#### Storage responsibilities (ldz)

1. **Flat directory layout.** A single `/models` directory at the mount root holds all currently-required model files across all currently-deployed engine versions. No subdirectories required by the contract (though ldz may organize internally however helps their backup / lifecycle tooling).

2. **Multi-version coexistence.** During a rolling deployment, both old-version and new-version model files MUST be present simultaneously. The PV / S3 prefix must be sized to hold the union of all active model versions, not just the latest.

3. **Population by one-time Kubernetes Job.** When a new engine version's manifest references a model file not yet in storage, ldz runs a one-time Job that uses aegis-core's `tools/scripts/download_models.sh` (containerized) to fetch + SHA-verify + place the file into the storage. Idempotent: re-running downloads only missing files.

4. **Lifecycle policy: prune by reference count.** A periodic Job lists all engine pods, reads each pod's bundled `manifest.json`, computes the union of referenced filenames, deletes any file in storage NOT in that union. Pruning is conservative — never delete a file currently referenced; reference list is the source of truth, not "the latest manifest".

   The lifecycle Job is owned by ldz (it's an infrastructure concern, not workload). aegis-core provides the manifest-walk logic as a small CLI (Phase 4c scope) so the Job is a simple `kubectl + that CLI + storage CRUD` shell rather than re-implementing manifest parsing in HCL/Helm.

### Why C over A / B

**Versus A (per-pod fetch)**: A's worst case is a 3-5 minute pod cold-start downloading ~1.5 GB on every restart. For an inference service where pod restarts happen on every deployment AND on every node-eviction, that's a per-incident operational cost paid forever. Storage at $0.16/month buys out of that cost permanently.

**Versus B (versioned subdirs)**: B requires a new path convention (`/models/<version>/<filename>`) that doesn't exist in the project today. C reuses the existing `models/manifest.json` schema verbatim — no new convention. B's `<version>` semantics also have to be defined (semver? git sha? release tag?), each choice introducing its own coupling between engine + storage.

**Why this matters for the storage shape**: ldz #82 originally framed the requirement as "directory at /models with manifest-required files". Approach C tightens this to "directory at /models holding the UNION of files required by ALL concurrently-running engine versions, sized accordingly". That changes ldz's storage sizing math from "size of one manifest" to "max realistic size during overlap window" — meaningfully larger if a 1.5 GB whisper model upgrades to a different 1.5 GB whisper model and both must coexist for 30 minutes.

## Consequences

### Positive

- Rolling deployment becomes a non-event: old pods read their files, new pods read theirs, no coordination needed in the moment.
- Storage shape is simple (flat dir, the cheapest abstraction across EBS / S3+CSI / EFS).
- Engine code change is small — Phase 4c "Engine startup manifest validation" (already on ROADMAP) just becomes "manifest-driven multi-required-model load" instead of "single-model assume-and-crash".
- Lifecycle pruning by reference count means we cannot accidentally prune a file an active pod is using, even if a developer manually edits the manifest.

### Negative

- Storage capacity must be sized for the deployment-overlap worst case, not the per-version case. For typical 2-version overlap × 2 GB per version = 4 GB ceiling — at $0.16/month for 2 GB EBS, the doubling is rounding noise, but it's load-bearing for the contract.
- The lifecycle prune Job is a real ldz-side responsibility that doesn't exist today. Until it lands, storage grows monotonically — manageable for the demo horizon but a Phase 4c blocker.
- The manifest-driven engine startup walk is engine code that doesn't exist today. Tracked in ROADMAP Phase 4c "Engine startup manifest validation" (this ADR upgrades that entry's scope).

### Out of scope (this ADR)

- The Bazel + image embedding of `models/manifest.json` into the engine binary — implementation detail for Phase 4c; could be `cc_embed_data` of the JSON, could be a sibling file in the image, etc.
- Model upgrade workflow inside the K8s Job that populates storage — straightforward `download + SHA-verify + atomic-rename` per existing `tools/scripts/download_models.sh` logic.
- Cross-region storage replication — orthogonal infra concern (ldz multi-region work, ADR-018-equivalent on their side).
