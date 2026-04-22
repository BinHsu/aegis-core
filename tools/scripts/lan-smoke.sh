#!/usr/bin/env bash
# Aegis Core — LAN smoke prerequisites + Qdrant supervisor.
#
# Checks + prepares everything the engine's RAG path needs before you run
# the full `bazel run //:app_local --with-frontend` demo. The only thing
# this script does NOT do is launch app_local itself — that step produces
# interleaved child output on stdout and is cleaner to run in its own
# terminal where you can Ctrl-C it.
#
# Sequence:
#
#   1. bge-m3 embedder present (safety net — verifies via
#      download_models.sh --verify-only; fetches with --model only if
#      still missing. Expected no-op when the user ran the default
#      download_models.sh at clone time per README §Quick Start)
#   2. Qdrant supervisor:
#        - If :6333 already healthy → treat as user-started external
#          instance; reuse + DO NOT manage its lifecycle.
#        - Else → fetch `./qdrant` if missing (4 OS/arch matrix),
#          start in background, wait for :6333/healthz, register
#          `trap EXIT INT TERM` that kills ONLY our own PID.
#   3. Taiwan corpus seeded into the local Qdrant collection
#   4. Print the final `bazel run //:app_local` command. If we started
#      Qdrant ourselves, the script then BLOCKS (wait on the qdrant
#      PID) so the user can run app_local in a second terminal;
#      Ctrl-C returns control and the trap cleans up. If Qdrant was
#      already running, the script exits immediately (preserves the
#      old one-shot UX for users who manage their own instance).
#
# Idempotent — every step short-circuits when already satisfied. See
# CONTRIBUTING.md §LAN smoke for the full human-in-the-loop flow.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

QDRANT_REST_PORT="${QDRANT_REST_PORT:-6333}"
QDRANT_GRPC_PORT="${QDRANT_GRPC_PORT:-6334}"
QDRANT_VERSION="${QDRANT_VERSION:-v1.17.1}"
QDRANT_RELEASE_BASE="https://github.com/qdrant/qdrant/releases/download/$QDRANT_VERSION"
CORPUS="$REPO_ROOT/docs/rag/taiwan.md"

# PID of the qdrant instance THIS SCRIPT started. Empty when we're
# reusing a user-started external instance; the trap must not touch
# that PID space.
LAN_SMOKE_QDRANT_PID=""

say() {
  printf '\033[1;34m[lan-smoke]\033[0m %s\n' "$*"
}

die() {
  printf '\033[1;31m[lan-smoke]\033[0m %s\n' "$*" >&2
  exit 1
}

# Return the tar.gz basename for the host's OS/arch, or fail. Keep
# this table in sync with https://github.com/qdrant/qdrant/releases —
# Qdrant ships 4 prebuilt binary archives covering the common LAN
# demo hardware (Apple Silicon, Intel Mac, x86 Linux, ARM Linux).
qdrant_tarball_for_host() {
  local os arch
  os="$(uname -s)"
  arch="$(uname -m)"
  case "$os/$arch" in
    Darwin/arm64)              echo "qdrant-aarch64-apple-darwin.tar.gz" ;;
    Darwin/x86_64)             echo "qdrant-x86_64-apple-darwin.tar.gz" ;;
    Linux/x86_64)              echo "qdrant-x86_64-unknown-linux-gnu.tar.gz" ;;
    Linux/aarch64|Linux/arm64) echo "qdrant-aarch64-unknown-linux-gnu.tar.gz" ;;
    *) return 1 ;;
  esac
}

ensure_qdrant_binary() {
  if [[ -x "$REPO_ROOT/qdrant" ]]; then
    return 0
  fi
  local tarball
  tarball="$(qdrant_tarball_for_host)" || \
    die "unsupported OS/arch ($(uname -s) $(uname -m)); install qdrant manually per docs/runbooks/qdrant-local-setup.md"
  say "  downloading Qdrant $QDRANT_VERSION ($tarball) — one-time, ~20 MB"
  # Pinned to a concrete release tag + HTTPS; no SHA-256 verification
  # yet (TODO: pin tarball digests when the ADR-0026-style third-party-
  # binary CAS pattern lands). Matches the posture of the runbook
  # snippet we're replacing, no regression.
  ( cd "$REPO_ROOT" && curl -fsSL "$QDRANT_RELEASE_BASE/$tarball" | tar xz )
  [[ -x "$REPO_ROOT/qdrant" ]] || \
    die "Qdrant tarball extracted but ./qdrant not found — unexpected layout"
}

wait_for_qdrant_ready() {
  local timeout_s=30 elapsed=0
  while (( elapsed < timeout_s )); do
    if curl -fsS "http://localhost:$QDRANT_REST_PORT/healthz" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    elapsed=$((elapsed + 1))
  done
  return 1
}

cleanup_qdrant() {
  # Trap handler — only kills the PID we started, NEVER user-external
  # instances (LAN_SMOKE_QDRANT_PID stays empty in that branch).
  if [[ -n "$LAN_SMOKE_QDRANT_PID" ]] \
       && kill -0 "$LAN_SMOKE_QDRANT_PID" 2>/dev/null; then
    say "  stopping supervised qdrant (pid $LAN_SMOKE_QDRANT_PID)"
    kill "$LAN_SMOKE_QDRANT_PID" 2>/dev/null || true
    wait "$LAN_SMOKE_QDRANT_PID" 2>/dev/null || true
  fi
}
trap cleanup_qdrant EXIT INT TERM

# --- Step 1: embedder -------------------------------------------------------

say "step 1/3 — bge-m3 embedder"
# Safety net: README Quick Start's default `download_models.sh` run
# already fetched bge-m3 (since 2026-04-22 the default covers every
# non-PLACEHOLDER model). Verify silently; only fall through to a real
# download if the CAS file is actually missing. Capture output via a
# variable to sidestep pipefail interactions with a non-matching
# `grep -q` in an `if` condition.
if ! verify_output=$("$REPO_ROOT/tools/scripts/download_models.sh" --verify-only 2>&1); then
  printf '%s\n' "$verify_output" >&2
  die "download_models.sh --verify-only failed (see above)"
fi
if printf '%s\n' "$verify_output" | grep -q "^\[miss\] bge-m3-q4km"; then
  say "  bge-m3 missing — fetching (438 MB, one-time)"
  "$REPO_ROOT/tools/scripts/download_models.sh" --model bge-m3-q4km
else
  say "  bge-m3 already present — skip"
fi

# --- Step 2: Qdrant supervisor ---------------------------------------------

say "step 2/3 — Qdrant on :$QDRANT_GRPC_PORT"
if curl -fsS "http://localhost:$QDRANT_REST_PORT/healthz" >/dev/null 2>&1; then
  say "  qdrant already running (external) → reuse, will NOT manage lifecycle"
else
  say "  qdrant not running — supervising a local instance"
  ensure_qdrant_binary
  # Start in a subshell so the `cd` is scoped and the qdrant data dir
  # lands at REPO_ROOT/.qdrant-data (gitignored). `exec` replaces the
  # subshell with qdrant itself so `$!` captures qdrant's real PID —
  # without `exec`, `$!` would point at the short-lived subshell
  # parent and killing it would leave qdrant orphaned under init.
  # Redirect stdout/err to /dev/null — qdrant is chatty on startup.
  ( cd "$REPO_ROOT" && exec ./qdrant --disable-telemetry >/dev/null 2>&1 ) &
  LAN_SMOKE_QDRANT_PID=$!
  if ! wait_for_qdrant_ready; then
    die "qdrant did not become ready on :$QDRANT_REST_PORT within 30s (pid $LAN_SMOKE_QDRANT_PID)"
  fi
  say "  qdrant up (pid $LAN_SMOKE_QDRANT_PID), REST :$QDRANT_REST_PORT → ok"
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
    seed --corpus="$CORPUS" --target=local --tenant=demo

# --- Next step ---------------------------------------------------------------

cat <<EOF

$(printf '\033[1;32m')[lan-smoke] prerequisites ready.$(printf '\033[0m')

Start the full stack in a separate terminal:

    export QDRANT_URL=localhost:$QDRANT_GRPC_PORT
    ./tools/bazelisk/bazelisk run //:app_local -- --with-frontend

Then:

  1. Open http://localhost:5173
  2. Create a meeting with rag_id=aegis_demo_taiwan (auto-populated
     in the Host UI dropdown via ListCorpora since PR #75)
  3. Scan the viewer QR on a phone on the same LAN
  4. Speak into the mic

EOF

if [[ -n "$LAN_SMOKE_QDRANT_PID" ]]; then
  cat <<EOF
$(printf '\033[1;33m')[lan-smoke] Qdrant is supervised by this script (pid $LAN_SMOKE_QDRANT_PID).$(printf '\033[0m')
Leave this terminal open while you run app_local. Press Ctrl-C here
when you're done with the demo — Qdrant will be stopped cleanly.
(If you'd rather manage Qdrant yourself, start it before running
this script and we'll detect + reuse it.)

EOF
  # Block until qdrant exits or user signals. Trap handles cleanup.
  wait "$LAN_SMOKE_QDRANT_PID" || true
fi
