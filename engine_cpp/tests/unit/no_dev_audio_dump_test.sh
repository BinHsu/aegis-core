#!/usr/bin/env bash
# ADR-0005 R7 enforcement: release binaries MUST NOT ship the
# AEGIS_DEV_AUDIO_DUMP debug banner.
#
# main.cc:138-145 guards a cerr banner with `#ifdef AEGIS_DEV_AUDIO_DUMP`.
# When -DAEGIS_DEV_AUDIO_DUMP is set (only under `--config=debug` per
# .bazelrc:80), the #ifdef block compiles in and the banner string
# "AEGIS_DEV_AUDIO_DUMP is enabled" lands in .rodata. `--strip=always`
# on release strips symbol tables but NOT .rodata, so the string
# survives and this grep still catches accidental leakage.
#
# Default `bazel test` is fastbuild (no define) — passes. Test fails
# if any build path outside `--config=debug` smuggles the copt in.

set -euo pipefail

BIN="${1:?binary path missing — invoke via sh_test with data = [:engine]}"

if [[ ! -f "$BIN" ]]; then
  echo "FAIL: binary not found at $BIN" >&2
  exit 1
fi

if LC_ALL=C grep -a -q "AEGIS_DEV_AUDIO_DUMP" "$BIN"; then
  cat >&2 <<EOF
FAIL: ADR-0005 R7 violation — binary contains AEGIS_DEV_AUDIO_DUMP string.

The #ifdef-gated debug banner leaked into a non-debug build. Somewhere
-DAEGIS_DEV_AUDIO_DUMP is being applied outside \`build:debug\`. Inspect
.bazelrc, per-target copts, and any --copt flags on the invoking CI
command line.

Banner source : engine_cpp/cmd/engine/main.cc:138-145
Defining flag : .bazelrc:80 (build:debug --copt=-DAEGIS_DEV_AUDIO_DUMP)
Binary checked: $BIN
EOF
  exit 1
fi

echo "OK: ADR-0005 R7 satisfied — no AEGIS_DEV_AUDIO_DUMP string in $BIN"
