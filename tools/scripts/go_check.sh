#!/usr/bin/env bash
# tools/scripts/go_check.sh — run `go fmt ./...` and `go vet ./...` inside
# every Go module listed in go.work, using the hermetic Bazel Go SDK.
#
# Contributors run this before commits touching Go code; CI (when wired
# in Phase 2+) calls the same script so behavior is identical.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Keep this list in sync with go.work's `use (...)` block.
MODULES=(
  gateway_go
)

fail=0
for m in "${MODULES[@]}"; do
  echo "=== $m ==="

  # go fmt writes changes in place and prints reformatted filenames to
  # stdout (nothing on clean files). Keep stderr un-captured so real
  # errors (including bazelisk's own banners) stay visible.
  out=$(cd "$REPO_ROOT/$m" && "$SCRIPT_DIR/go.sh" fmt ./...)
  if [[ -n "$out" ]]; then
    echo "[fmt] reformatted:"
    echo "$out" | sed 's/^/    /'
    fail=1
  else
    echo "[fmt] clean"
  fi

  if (cd "$REPO_ROOT/$m" && "$SCRIPT_DIR/go.sh" vet ./...); then
    echo "[vet] clean"
  else
    echo "[vet] FAILED"
    fail=1
  fi
done

if [[ $fail -eq 0 ]]; then
  echo "All Go modules clean."
fi
exit $fail
