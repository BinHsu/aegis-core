#!/usr/bin/env bash
# tools/scripts/frontend.sh — thin wrapper around pnpm / vite / tsc
# that routes every invocation through Bazel-managed Node (see
# ADR-0015). Using this wrapper (rather than host-installed `npm`)
# is how the repo honors CLAUDE.md Rule 6 for the frontend toolchain.
#
# Usage:
#   ./tools/scripts/frontend.sh install      — run pnpm install
#   ./tools/scripts/frontend.sh dev          — start vite dev server
#   ./tools/scripts/frontend.sh build        — production build
#   ./tools/scripts/frontend.sh typecheck    — run tsc --noEmit
#   ./tools/scripts/frontend.sh <anything>   — pass-through to pnpm
#
# The script prepends the Bazel-managed Node binary to PATH for the
# duration of the command, so `pnpm` / `npx` / `vite` all resolve to
# hermetic versions and never leak to a host-installed Node.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$REPO_ROOT"

# Resolve the Bazel-managed Node binary's directory. We build the
# @nodejs_toolchains//:resolved_toolchain target once per session;
# subsequent invocations hit the Bazel action cache and are ~free.
# `bazel info output_base` points at the place Bazel extracted the
# toolchain; we probe a couple of known subpaths because the exact
# layout changes across rules_nodejs versions.
resolve_node_bin_dir() {
  local output_base
  output_base="$("$REPO_ROOT/tools/bazelisk/bazelisk" info output_base 2>/dev/null)"
  # Typical layouts under rules_nodejs + aspect_rules_js toolchain:
  for candidate in \
    "$output_base/external/rules_nodejs~~node~nodejs_darwin_arm64/bin" \
    "$output_base/external/rules_nodejs~~node~nodejs_darwin_amd64/bin" \
    "$output_base/external/rules_nodejs~~node~nodejs_linux_amd64/bin" \
    "$output_base/external/rules_nodejs~~node~nodejs_linux_arm64/bin"
  do
    if [[ -x "$candidate/node" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

# Ensure the Node toolchain is downloaded. First-run cost is ~30s
# on a cold Bazel cache; subsequent runs are cache hits.
"$REPO_ROOT/tools/bazelisk/bazelisk" build \
  @rules_nodejs//nodejs:resolved_toolchain >/dev/null 2>&1 || true

NODE_BIN_DIR="$(resolve_node_bin_dir || true)"
if [[ -z "${NODE_BIN_DIR:-}" ]]; then
  cat >&2 <<EOF
error: could not locate the Bazel-managed Node binary.

Tried several candidate paths under \`bazel info output_base\`; none
contained an executable \`node\`. This usually means the
@rules_nodejs toolchain has not been fetched yet, or its
repo-mapping naming changed across a rules_nodejs version bump.

Try:
  1. $REPO_ROOT/tools/bazelisk/bazelisk fetch @rules_nodejs//nodejs:resolved_toolchain
  2. inspect \`ls \$(./tools/bazelisk/bazelisk info output_base)/external/ | grep nodejs\`
  3. adjust the candidate-path list in this script if the layout
     differs.
EOF
  exit 1
fi

# Bazel-managed pnpm — built once, then available as a plain binary.
"$REPO_ROOT/tools/bazelisk/bazelisk" build @pnpm//:pnpm >/dev/null 2>&1
PNPM_BIN="$REPO_ROOT/bazel-bin/external/aspect_rules_js~~pnpm~pnpm/pnpm_/pnpm"

# Prepend Node to PATH for the subprocess only. The pnpm binary we
# invoke below will shell out to `node` internally (it's a Node
# program itself); having it find the hermetic node is the whole
# point of this wrapper.
export PATH="$NODE_BIN_DIR:$PATH"

# aspect_rules_js's js_binary wrapper refuses to run without
# BAZEL_BINDIR set — it uses the value to cd into the bazel-out
# tree before executing. For an out-of-band CLI invocation like
# ours (we're not inside a Bazel action), "." is the sanctioned
# escape value the rules_js README documents. Without this, pnpm
# exits immediately with FATAL: BAZEL_BINDIR must be set.
export BAZEL_BINDIR="."

cmd="${1:-help}"
shift || true

case "$cmd" in
  install)
    # `install` runs pnpm on the repo-root workspace so all members
    # (currently just frontend_web/) get their deps.
    exec "$PNPM_BIN" --dir "$REPO_ROOT" install "$@"
    ;;
  dev|build|typecheck|preview)
    # Every other "named" command delegates to pnpm scripts inside
    # frontend_web's package.json.
    exec "$PNPM_BIN" --dir "$REPO_ROOT/frontend_web" run "$cmd" "$@"
    ;;
  help|-h|--help|"")
    cat <<EOF
Aegis Core frontend wrapper (ADR-0015 hermetic Node).

Commands:
  install                 Install all workspace deps via pnpm.
  dev                     Start Vite dev server (default port 5173).
  build                   Production build → frontend_web/dist/.
  typecheck               Run tsc --noEmit across the frontend tree.
  preview                 Preview the production build locally.
  <anything else>         Passed through to pnpm with --dir frontend_web.

Every invocation uses the Bazel-managed Node toolchain; host Node
installations are never consulted. See ADR-0015 for rationale.
EOF
    ;;
  *)
    # Pass-through mode: anything we don't recognize goes straight
    # to pnpm. Lets ad-hoc commands like `./frontend.sh add left-pad`
    # work without extending the case list.
    exec "$PNPM_BIN" --dir "$REPO_ROOT/frontend_web" "$cmd" "$@"
    ;;
esac
