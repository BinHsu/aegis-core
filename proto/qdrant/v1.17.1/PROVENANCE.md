# Qdrant proto vendor — provenance

## Source

- **Upstream**: [`qdrant/qdrant`](https://github.com/qdrant/qdrant)
- **Tag**: `v1.17.1`
- **Released**: 2026-03-27
- **License**: Apache-2.0
  ([upstream LICENSE](https://github.com/qdrant/qdrant/blob/v1.17.1/LICENSE))
- **Source path in upstream**: `lib/api/src/grpc/proto/`
- **Tarball SHA-256** (from
  `https://github.com/qdrant/qdrant/archive/refs/tags/v1.17.1.tar.gz`):

      32012fab5334b10f20fec5b41a9e9907f9b232cb78d55faa8c1fe6c8f8535629

## Files included (closure over our import graph)

| File | Purpose |
| --- | --- |
| `collections.proto` | Collection message types |
| `collections_service.proto` | Collection service RPCs |
| `points.proto` | Point + Vector + Filter message types |
| `points_service.proto` | Point service RPCs (Upsert / Search / Query) |
| `qdrant_common.proto` | Shared types |
| `json_with_int.proto` | Qdrant's JSON-with-int payload type |

## Files intentionally not included

| File | Why excluded |
| --- | --- |
| `*_internal_service.proto` | Cluster-internal coordination — not for external clients |
| `raft_service.proto` | Raft consensus — cluster-internal |
| `shard_snapshots_service.proto`, `snapshots_service.proto` | Snapshot ops — not in our RAG seed/query path |
| `telemetry_internal.proto` | Internal telemetry |
| `health_check.proto` | We surface health via our own engine gRPC, not the Qdrant channel |
| `qdrant.proto`, `qdrant_internal_service.proto` | Aggregate / internal — not needed by external clients |

## Why check-in rather than http_archive vendor

Our initial design (see removed stanza in `MODULE.bazel`) used
`http_archive` to fetch the full Qdrant source tarball at a pinned
tag, then exposed a `proto_library` via an external BUILD file with
`strip_import_prefix = "lib/api/src/grpc/proto"` so the protos'
bare-filename imports (`import "qdrant_common.proto";`) would resolve.

That ran into a known gRPC `cc_grpc_library` quirk: the rule resolves
proto source paths through `_virtual_imports/<name>/…` when
`strip_import_prefix` is applied, but the grpc C++ codegen plugin
reads the source path directly and does not follow the virtual
mapping — protoc fails with `Could not make proto path relative: …
_virtual_imports/qdrant_proto/collections.proto: No such file or
directory`. Putting the `cc_grpc_library` in the same Bazel package
as the `proto_library` (inside the external archive's BUILD) fixed
the "proto does not lie within package" analysis error but not the
virtual-imports protoc invocation error.

Checking in the 6 proto files eliminates the class of problem
entirely, at the cost of manual copy on upgrade. The upgrade
procedure is cheap (see next section).

## Upgrade procedure

1. Pick the new Qdrant release tag from
   <https://github.com/qdrant/qdrant/releases>.
2. Bump the directory — e.g., `proto/qdrant/v1.17.1/` →
   `proto/qdrant/v1.18.0/`. Do NOT overwrite the old directory in
   the same commit; keep it until the bump has landed, for rollback
   clarity.
3. Fetch the new tarball, recompute its SHA-256, update this file.
4. Copy the 6 files above from the new tag's
   `lib/api/src/grpc/proto/`. If the new tag adds a proto that our
   closure now depends on, add it to the list here and to
   `BUILD.bazel:srcs`.
5. Run `./tools/bazelisk/bazelisk test //engine_cpp/...` locally —
   regenerated C++ stubs must still compile against
   `engine_cpp/src/vectordb/qdrant_client.cc`. If Qdrant introduced a
   breaking proto change, patch the client in the same PR.
6. PR title: `deps(qdrant-proto): bump v<old> → v<new>`.
