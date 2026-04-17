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
./tools/buf/buf generate

# Post-process the connect-es output. Connect-ES v1.6.1's
# `target=ts` generator unconditionally emits `// @ts-nocheck` on its
# service files, which silently disables type checking and turns
# every method's response into `Message<unknown>` at consume sites.
# That defeats the whole point of generating TypeScript. Strip the
# directive so callers get real types. (Fixed in upstream v2; remove
# this when the BSR plugin catches up — see buf.gen.yaml.)
TS_FILES=$(find frontend_web/src/gen -name "*.ts" 2>/dev/null || true)
if [[ -n "$TS_FILES" ]]; then
  # Use perl rather than sed -i because BSD sed and GNU sed disagree
  # on the -i empty-string argument; perl is uniform across both.
  perl -i -ne 'print unless m{^// \@ts-nocheck\s*$}' $TS_FILES
fi
