#!/usr/bin/env bash
# Aegis Core — LAN smoke prerequisites.
#
# Checks + prepares everything the engine's RAG path needs before you run
# the full `bazel run //:app_local --with-frontend` demo. The only thing
# this script does NOT do is launch app_local itself — that step produces
# interleaved child output on stdout and is cleaner to run in its own
# terminal where you can Ctrl-C it.
#
# Sequence:
#
#   1. bge-m3 embedder present (via download_models.sh --model)
#   2. Qdrant reachable on :6334 (fail-fast if not; see runbook)
#   3. Taiwan corpus seeded into the local Qdrant collection
#   4. Print the final `bazel run //:app_local` command to copy-paste
#
# Idempotent — every step short-circuits when already satisfied. See
# CONTRIBUTING.md §LAN smoke for the full human-in-the-loop flow.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

QDRANT_REST_PORT="${QDRANT_REST_PORT:-6333}"
QDRANT_GRPC_PORT="${QDRANT_GRPC_PORT:-6334}"
CORPUS="$REPO_ROOT/docs/rag/taiwan.md"

say() {
  printf '\033[1;34m[lan-smoke]\033[0m %s\n' "$*"
}

die() {
  printf '\033[1;31m[lan-smoke]\033[0m %s\n' "$*" >&2
  exit 1
}

# --- Step 1: embedder -------------------------------------------------------

say "step 1/3 — bge-m3 embedder"
"$REPO_ROOT/tools/scripts/download_models.sh" --model bge-m3-q4km

# --- Step 2: Qdrant health probe -------------------------------------------

say "step 2/3 — Qdrant on :$QDRANT_GRPC_PORT"
if curl -fsS "http://localhost:$QDRANT_REST_PORT/healthz" >/dev/null 2>&1; then
  say "  qdrant REST :$QDRANT_REST_PORT → ok"
else
  cat >&2 <<EOF
[lan-smoke] qdrant not reachable on localhost:$QDRANT_REST_PORT (REST probe).

  Start it per docs/runbooks/qdrant-local-setup.md. Shortest path on
  Apple Silicon:

    cd $REPO_ROOT
    curl -sL \\
      https://github.com/qdrant/qdrant/releases/download/v1.17.1/qdrant-aarch64-apple-darwin.tar.gz \\
      | tar xz
    ./qdrant --disable-telemetry >/dev/null 2>&1 &

  Then re-run this script.
EOF
  exit 1
fi

# --- Step 3: seed corpus ---------------------------------------------------

say "step 3/3 — seed $CORPUS into Qdrant (target=local)"
[ -f "$CORPUS" ] || die "corpus not found at $CORPUS"

# The seed subcommand reads AEGIS_MANIFEST_PATH / AEGIS_MODEL_PATH to
# locate the embedder via CAS layout (ADR-0026). Absolute paths so the
# engine binary can resolve them regardless of its runfiles-relative CWD.
AEGIS_MANIFEST_PATH="$REPO_ROOT/models/manifest.json" \
AEGIS_MODEL_PATH="$REPO_ROOT/models" \
  "$REPO_ROOT/tools/bazelisk/bazelisk" run //engine_cpp/cmd/engine -- \
    seed --corpus="$CORPUS" --target=local

# --- Next step ---------------------------------------------------------------

cat <<EOF

\033[1;32m[lan-smoke] prerequisites ready.\033[0m

Start the full stack in a separate terminal:

    export QDRANT_URL=localhost:$QDRANT_GRPC_PORT
    ./tools/bazelisk/bazelisk run //:app_local -- --with-frontend

Then:

  1. Open http://localhost:5173
  2. Create a meeting with rag_id=aegis_taiwan
  3. Scan the viewer QR on a phone on the same LAN
  4. Speak into the mic

EOF
