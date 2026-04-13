#!/usr/bin/env bash
# tools/scripts/go.sh — hermetic `go` invocation via the Bazel-managed Go SDK.
#
# Per CLAUDE.md Rule 6, Aegis Core does not require a system Go install.
# Contributors run Go commands through this wrapper, which builds the
# Go toolchain binary out of rules_go's hermetic SDK and then invokes
# it with the repo root as the working directory so module paths like
# `./gateway_go/...` resolve correctly.
#
# Usage:
#   ./tools/scripts/go.sh fmt ./gateway_go/...
#   ./tools/scripts/go.sh vet ./gateway_go/...
#   ./tools/scripts/go.sh mod tidy           # inside gateway_go/
#   ./tools/scripts/go.sh version
#
# For commands that need to run inside the module root (like `mod tidy`),
# cd into that directory first, then invoke this script.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
CALLER_PWD="$(pwd)"

# Build the Go tool wrapper once (from the repo root — Bazel requires it).
# Bazel caches the artifact so subsequent invocations are no-ops.
(
  cd "$REPO_ROOT"
  ./tools/bazelisk/bazelisk build --ui_event_filters=-info,-stdout,-stderr \
      --noshow_progress -- @rules_go//go >/dev/null
)

# Locate the produced go binary. Bazel's external repo naming includes a
# tilde-separated segment that depends on the Bazel version (7.x uses
# "rules_go~"; 8.x may use "rules_go+"). Probe both.
GO_BIN=""
for candidate in \
    "$REPO_ROOT/bazel-bin/external/rules_go~/go/tools/go_bin_runner/bin/go" \
    "$REPO_ROOT/bazel-bin/external/rules_go+/go/tools/go_bin_runner/bin/go"; do
  if [[ -x "$candidate" ]]; then
    GO_BIN="$candidate"
    break
  fi
done

if [[ -z "$GO_BIN" ]]; then
  echo "tools/scripts/go.sh: cannot locate Go binary in bazel-bin/." >&2
  echo "  Searched:" >&2
  echo "    bazel-bin/external/rules_go~/go/tools/go_bin_runner/bin/go" >&2
  echo "    bazel-bin/external/rules_go+/go/tools/go_bin_runner/bin/go" >&2
  echo "  Check that the build above succeeded." >&2
  exit 1
fi

# Invoke from the caller's cwd so package-relative paths (e.g., `./...`
# from inside a module directory) resolve correctly. Module-scoped
# commands like `go fmt ./...` and `go mod tidy` require the caller to
# cd into the module root first — see tools/scripts/go_check.sh for a
# helper that loops over every Go module for common checks.
#
# BUILD_WORKSPACE_DIRECTORY is set by `bazel run`; rules_go's
# go_bin_runner uses it to locate go.mod. When invoked outside
# bazel run (i.e. directly via this wrapper) it must be set
# manually or the binary errors with "open gateway_go/go.mod: no
# such file or directory" regardless of cwd.
cd "$CALLER_PWD"
export BUILD_WORKSPACE_DIRECTORY="$REPO_ROOT"
exec "$GO_BIN" "$@"
