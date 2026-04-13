#!/usr/bin/env bash
# tools/scripts/proto_gen.sh — regenerate checked-in .pb.go files
# from proto/aegis/v1/ per ADR-0013. Run this whenever .proto changes;
# commit the regenerated files alongside the .proto diff.
#
# CI runs the same script and then `git diff --exit-code` to fail
# PRs where the generated tree drifted from the .proto source.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$REPO_ROOT"
exec ./tools/buf/buf generate
