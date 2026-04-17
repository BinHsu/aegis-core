#!/usr/bin/env bash
# Aegis Core — ADR-0021 P3 version-match check for the shared ggml runtime.
#
# The standalone @ggml, @whisper_cpp, and @llama_cpp archives all bundle
# (or ARE) the ggml tensor library. They declare a GGML_VERSION in
# `ggml/CMakeLists.txt`. Bumping one without the other two causes link
# failures when llama.cpp or whisper.cpp references symbols the standalone
# ggml doesn't export (see docs/incidents.md #10).
#
# This script runs `bazelisk fetch` for the three external archives, then
# greps each for its declared ggml version. It prints the three versions
# side-by-side and exits:
#   0 — all three versions identical
#   1 — versions diverge OR any archive is missing (likely an upgrade
#       that skipped bumping a sibling; developer must review)
#
# IMPORTANT caveat: an identical version STRING is not a guarantee of
# source-level API parity. ggml-org re-uses version numbers across
# cherry-pick patches (e.g. llama.cpp b8595 shipped "ggml 0.9.8" with
# `_ptr` symbols that weren't in the standalone v0.9.8 tag). This script
# catches the easy case — three different version numbers. The harder
# case — same number, divergent source — is caught by the CI job that
# actually builds //engine_cpp/tests/integration:all, where the link step
# fails if symbol tables don't align.
#
# Usage:
#   ./tools/scripts/check_ggml_versions.sh          # fetch + check
#   ./tools/scripts/check_ggml_versions.sh --quiet  # no output on success

set -euo pipefail

QUIET=0
if [[ "${1:-}" == "--quiet" ]]; then
  QUIET=1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
cd "$REPO_ROOT"

# Fetch the three archives via Bazel. Uses existing http_archive entries
# from MODULE.bazel — no duplication of URLs / SHAs.
if [[ "$QUIET" == "0" ]]; then
  echo "Fetching @ggml, @whisper_cpp, @llama_cpp via Bazel..."
fi
./tools/bazelisk/bazelisk fetch \
  @ggml//... @whisper_cpp//... @llama_cpp//... >/dev/null 2>&1

# Resolve the Bazel external repo root. Newer Bazel (bzlmod) uses
# `bazel info output_base` + `/external/` + a canonical repo name with
# `~` or `+` separators depending on Bazel version. Probe for both.
OUTPUT_BASE="$(./tools/bazelisk/bazelisk info output_base 2>/dev/null)"

find_cmakelists() {
  local repo_stem="$1"     # ggml | whisper_cpp | llama_cpp
  local rel_path="$2"      # "" for standalone ggml; "ggml" for bundled
  local sub=""
  if [[ -n "$rel_path" ]]; then
    sub="/${rel_path}"
  fi
  # Try the two bzlmod canonical layouts + the legacy non-bzlmod form.
  local candidates=(
    "$OUTPUT_BASE/external/_main~_repo_rules~${repo_stem}${sub}/CMakeLists.txt"
    "$OUTPUT_BASE/external/+_repo_rules+${repo_stem}${sub}/CMakeLists.txt"
    "$OUTPUT_BASE/external/${repo_stem}${sub}/CMakeLists.txt"
  )
  for p in "${candidates[@]}"; do
    if [[ -f "$p" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

extract_version() {
  local file="$1"
  local major minor patch
  major=$(grep -E '^set\(GGML_VERSION_MAJOR ' "$file" | awk '{print $2}' | tr -d ')')
  minor=$(grep -E '^set\(GGML_VERSION_MINOR ' "$file" | awk '{print $2}' | tr -d ')')
  patch=$(grep -E '^set\(GGML_VERSION_PATCH ' "$file" | awk '{print $2}' | tr -d ')')
  if [[ -z "$major" || -z "$minor" || -z "$patch" ]]; then
    echo "UNKNOWN"
  else
    echo "${major}.${minor}.${patch}"
  fi
}

GGML_CM=$(find_cmakelists "ggml" "" || true)
WHISPER_CM=$(find_cmakelists "whisper_cpp" "ggml" || true)
LLAMA_CM=$(find_cmakelists "llama_cpp" "ggml" || true)

MISSING=0
for pair in "ggml:$GGML_CM" "whisper_cpp:$WHISPER_CM" "llama_cpp:$LLAMA_CM"; do
  name="${pair%%:*}"
  path="${pair#*:}"
  if [[ -z "$path" ]]; then
    echo "ERROR: could not locate $name ggml/CMakeLists.txt under $OUTPUT_BASE/external/" >&2
    MISSING=1
  fi
done
if [[ "$MISSING" == "1" ]]; then
  exit 1
fi

GGML_V=$(extract_version "$GGML_CM")
WHISPER_V=$(extract_version "$WHISPER_CM")
LLAMA_V=$(extract_version "$LLAMA_CM")

# Convert major.minor.patch → a sortable integer so `<` means "older".
# Encodes as major*1_000_000 + minor*1_000 + patch (no leading zeros,
# avoiding bash's octal-literal interpretation). Safe up to x.999.999.
version_key() {
  local v="$1"
  if [[ "$v" == "UNKNOWN" ]]; then
    echo "-1"
    return
  fi
  local IFS=.
  read -r a b c <<<"$v"
  echo $(( a * 1000000 + b * 1000 + c ))
}

GGML_K=$(version_key "$GGML_V")
WHISPER_K=$(version_key "$WHISPER_V")
LLAMA_K=$(version_key "$LLAMA_V")

if [[ "$QUIET" == "0" ]]; then
  echo
  echo "ggml version declarations:"
  printf "  %-15s %s\n" "ggml"        "$GGML_V"
  printf "  %-15s %s\n" "whisper.cpp" "$WHISPER_V"
  printf "  %-15s %s\n" "llama.cpp"   "$LLAMA_V"
  echo
fi

# Hard-fail the case that actually breaks the link: standalone ggml is
# OLDER than what a consumer's vendored tree expects. The consumer's
# model-loader will reference symbols the standalone ggml doesn't export
# (see incident-10). The opposite case (standalone AHEAD of consumers) is
# backward-compatible since ggml API evolution is largely additive.
if [[ "$GGML_K" -lt "$WHISPER_K" ]]; then
  echo "ERROR: @ggml ($GGML_V) is OLDER than @whisper_cpp's bundled ggml ($WHISPER_V)." >&2
  echo "       This direction breaks the link. Bump @ggml to >= $WHISPER_V." >&2
  echo "       See ADR-0021 + docs/incidents.md #10." >&2
  exit 1
fi
if [[ "$GGML_K" -lt "$LLAMA_K" ]]; then
  echo "ERROR: @ggml ($GGML_V) is OLDER than @llama_cpp's bundled ggml ($LLAMA_V)." >&2
  echo "       This direction breaks the link. Bump @ggml to >= $LLAMA_V." >&2
  echo "       See ADR-0021 + docs/incidents.md #10." >&2
  exit 1
fi

if [[ "$QUIET" == "0" ]]; then
  if [[ "$GGML_K" -gt "$WHISPER_K" || "$GGML_K" -gt "$LLAMA_K" ]]; then
    echo "OK — standalone @ggml ($GGML_V) is ahead of consumers' vendored"
    echo "trees (whisper $WHISPER_V, llama $LLAMA_V). This direction is"
    echo "link-safe because ggml API growth is additive; older consumers"
    echo "reference a subset of newer ggml's symbols."
  else
    echo "OK — all three declare ggml v${GGML_V}."
  fi
  echo
  echo "Note: version STRING parity does not guarantee API parity"
  echo "(incident-10: llama b8595 shipped 'ggml 0.9.8' with _ptr symbols"
  echo "not present in the standalone v0.9.8 tag). The authoritative"
  echo "link-compatibility gate is //engine_cpp/tests/integration:all."
fi
