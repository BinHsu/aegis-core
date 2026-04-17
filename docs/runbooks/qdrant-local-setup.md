# Runbook — Qdrant Local Setup (for developers)

| Field | Value |
| --- | --- |
| Audience | **Any developer** who wants to exercise the engine's RAG path locally or run `//engine_cpp/tests/integration:qdrant_client_test`. |
| Applies to | ADR-0020 (engine owns inference) + Phase 3b Slice 5 (QdrantClient). |
| Not applicable to | Forkers who only want to build / run unit tests. Local Qdrant is required only for integration testing and the `engine --seed` / query paths (Slice 6+). |
| Estimated time | 3–5 minutes |
| Cost | $0 — runs entirely on your machine |
| Last reviewed | 2026-04-17 |

## Purpose

Stand up a Qdrant gRPC server on `localhost:6334` so the QdrantClient
integration test + the (forthcoming) `engine --seed --target=local`
CLI have a backend to talk to. The proto vendor at
`proto/qdrant/v1.17.1/` pins the client-side API shape; this runbook
stands up a server-side instance that speaks the same API.

Two paths below — **binary is preferred** because it avoids Docker
Desktop's VM memory overhead (~2–4 GB on macOS). Docker is a
drop-in alternative for developers who already prefer a containerized
workflow.

## Path 1 — prebuilt binary (preferred)

Qdrant publishes static x86_64 and arm64 binaries on every GitHub
release. The Rust build is compact (~150 MB RSS at idle) and has no
runtime dependencies beyond libc.

### macOS (Apple Silicon, arm64)

```bash
cd $(git rev-parse --show-toplevel)

curl -sL \
  https://github.com/qdrant/qdrant/releases/download/v1.17.1/qdrant-aarch64-apple-darwin.tar.gz \
  | tar xz

# Defaults to listening on :6333 (REST) + :6334 (gRPC) and storing
# data under ./storage/. Both paths are gitignored per this repo's
# .gitignore; Rule 6 is honored because everything stays inside the
# repo tree.
./qdrant --disable-telemetry >/dev/null 2>&1 &
echo "qdrant pid: $!"
```

If you see a port conflict, kill the previous process or pass
`--config-path <yaml>` with custom ports.

### Linux (x86_64)

```bash
cd $(git rev-parse --show-toplevel)

curl -sL \
  https://github.com/qdrant/qdrant/releases/download/v1.17.1/qdrant-x86_64-unknown-linux-musl.tar.gz \
  | tar xz

./qdrant --disable-telemetry >/dev/null 2>&1 &
echo "qdrant pid: $!"
```

### Windows (via WSL2)

Follow the Linux instructions above inside your WSL2 Ubuntu shell.
Native Windows is not tested — see
[CONTRIBUTING.md §Native Windows support](../../CONTRIBUTING.md#native-windows-support-known-gap).

### Version pin

This runbook pins **v1.17.1** to match
`proto/qdrant/v1.17.1/PROVENANCE.md`. The client protos and server
binary should track the same minor version (Qdrant keeps gRPC
backward-compatible within a minor release). When the proto vendor
bumps, bump this runbook in the same PR.

## Path 2 — Docker (alternative)

Use this if you already have Docker Desktop running and prefer
container isolation. On macOS, budget ~2–4 GB extra RAM for the
Docker Desktop Linux VM before Qdrant itself.

```bash
cd $(git rev-parse --show-toplevel)
mkdir -p .qdrant-data

docker run -d --name aegis-qdrant \
  -p 6333:6333 -p 6334:6334 \
  -v "$(pwd)/.qdrant-data:/qdrant/storage" \
  qdrant/qdrant:v1.17.1
```

Stop + clean:

```bash
docker stop aegis-qdrant && docker rm aegis-qdrant
```

## Verify the server is up

```bash
# REST health probe — should print {"status":"ok", ...}
curl -s http://localhost:6333/healthz

# Or via the integration test (auto-skips if QDRANT_URL unset).
QDRANT_URL=localhost:6334 \
  ./tools/bazelisk/bazelisk test \
    //engine_cpp/tests/integration:qdrant_client_test \
    --test_env=QDRANT_URL \
    --test_output=errors
```

The integration test exercises `CreateCollection` + `UpsertPoints` +
`Search` end-to-end against the running Qdrant. If all cases pass,
your local Qdrant is wired correctly.

## Shut down

### Binary path

```bash
# find the pid you printed at startup, or:
pkill -f "^./qdrant "
```

### Docker path

```bash
docker stop aegis-qdrant
```

## Directory confinement (CLAUDE.md Rule 6 note)

Both paths keep Qdrant's state under the repo tree. `.gitignore`
guards every artifact Qdrant's tarball + runtime creates at the
repo root (`/qdrant`, `/config`, `/LICENSE_BSL_4.0.txt`,
`/storage/`, `/.qdrant-initialized`, `/.qdrant-data/`). Nothing
leaks to your home directory or `/usr/local/`. The Docker path
adds a runtime-service dependency on the `qdrant/qdrant:v1.17.1`
image cache in Docker daemon storage — same out-of-tree situation
as `docker run postgres:16` for a backend engineer — an
acknowledged pragmatic exception to Rule 6's strict reading
because `qdrant/qdrant` is a service we CONNECT to, not a
build-time dependency.

## Troubleshooting

- **`Address already in use`**: another Qdrant instance, an old
  container, or some unrelated service is on :6334. `lsof -i :6334`
  / `docker ps` to find it.
- **`permission denied` on `.qdrant-data/`**: the Docker path runs
  Qdrant as a non-root user inside the container. `chmod -R a+w
  .qdrant-data/` after first run, or use the binary path.
- **Integration test `UNAVAILABLE` / channel errors**: Qdrant's
  gRPC port is `:6334`, REST is `:6333`. Using `:6333` in
  `QDRANT_URL` sends gRPC traffic to an HTTP listener, which fails
  with an opaque unavailable error. Use `:6334`.

## Related

- [`PROVENANCE.md`](../../proto/qdrant/v1.17.1/PROVENANCE.md) — why
  v1.17.1 is the pinned version and how to upgrade the triple.
- [`docs/runbooks/qdrant-cloud-setup.md`](qdrant-cloud-setup.md) —
  Qdrant Cloud free-tier setup, for when you want to exercise the
  cloud path instead of local.
