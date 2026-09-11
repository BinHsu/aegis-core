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

# No post-processing. Earlier revisions stripped a `// @ts-nocheck`
# line that connect-es v1.6.1's `target=ts` generator emitted into its
# service files, which disabled type checking on every generated RPC
# signature. protoc-gen-es v2 does not emit that directive and the
# connect-es generator is gone entirely (see buf.gen.yaml), so the
# checked-in tree is now byte-for-byte what the generator produced.
# Keeping a no-op rewrite here would mean a future generator change
# gets silently edited instead of showing up in review.
#
# "Byte-for-byte" is a requirement, not an aspiration: the
# proto-codegen-drift job in ci-baseline.yml re-runs this script and
# diffs the result. Anything that rewrites the generated tree after the
# fact breaks that check, which is why .pre-commit-config.yaml excludes
# frontend_web/src/gen/ from prettier and from the whitespace / EOF
# fixers. Do not "tidy" generated output.
