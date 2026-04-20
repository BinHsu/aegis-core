#!/usr/bin/env bash
# Aegis Core — model download + SHA-256 verification (CAS layout).
#
# Reads models/manifest.json, downloads each model's file from origin_url
# and places it at the CAS path:
#
#     models/<id>/<sha256>.<ext>
#
# where <ext> is the filename extension. Idempotent: already-correct files
# are skipped. Per ADR-0026 (content-addressable storage) + ARCH §10.1.
#
# Migration from the historical flat layout (`models/<filename>`) is
# automatic: if the flat-path file already exists with the correct SHA,
# it is moved (not re-downloaded) to its new CAS path.
#
# Usage:
#   ./tools/scripts/download_models.sh                 # all required=true models
#   ./tools/scripts/download_models.sh --model <id>    # one model by id
#   ./tools/scripts/download_models.sh --verify-only   # no download, just check
#   ./tools/scripts/download_models.sh --all           # include optional (required=false) models
#
# The engine CAS preflight walker (engine_cpp/src/models/manifest_loader)
# verifies SHA-256 at startup; this script is the pre-flight that saves
# the user a failed engine boot.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MODELS_DIR="$REPO_ROOT/models"
MANIFEST="$MODELS_DIR/manifest.json"

MODE="required"    # required | all | one | verify
ONLY_ID=""
VERIFY_ONLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)        MODE="one"; ONLY_ID="${2:-}"; shift 2 ;;
    --all)          MODE="all"; shift ;;
    --verify-only)  VERIFY_ONLY=1; shift ;;
    --help|-h)
      sed -n '3,24p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

if [[ ! -f "$MANIFEST" ]]; then
  echo "ERROR: $MANIFEST not found" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required. Install via 'brew install jq' (macOS) or 'apt install jq' (Linux)." >&2
  exit 1
fi

# Build the jq filter based on mode.
case "$MODE" in
  required) FILTER='.models[] | select(.required == true)' ;;
  all)      FILTER='.models[]' ;;
  one)      FILTER=".models[] | select(.id == \"$ONLY_ID\")" ;;
esac

# Extract filename extension (".bin" / ".gguf" / ...); empty if none.
# Uses the portion after the LAST '.' to match manifest_loader's Extension().
extension_of() {
  local name="$1"
  if [[ "$name" == *.* ]]; then
    echo ".${name##*.}"
  else
    echo ""
  fi
}

# Process each matching model entry.
jq -c "$FILTER" "$MANIFEST" | while read -r entry; do
  id=$(jq -r '.id' <<<"$entry")
  filename=$(jq -r '.filename' <<<"$entry")
  sha256=$(jq -r '.sha256' <<<"$entry")
  size=$(jq -r '.size_bytes' <<<"$entry")
  url=$(jq -r '.origin_url' <<<"$entry")

  # Skip entries with placeholder metadata (aspirational manifest entries
  # whose model has not been pinned yet).
  if [[ "$sha256" == PLACEHOLDER* || "$filename" == PLACEHOLDER* ]]; then
    echo "[skip] $id — manifest entry still has PLACEHOLDER fields"
    continue
  fi

  ext=$(extension_of "$filename")
  cas_dir="$MODELS_DIR/$id"
  cas_path="$cas_dir/${sha256}${ext}"
  legacy_flat_path="$MODELS_DIR/$filename"

  # Already at the CAS path AND hash matches → trust-but-verify.
  if [[ -f "$cas_path" ]]; then
    have=$(shasum -a 256 "$cas_path" | awk '{print $1}')
    if [[ "$have" == "$sha256" ]]; then
      echo "[ok]   $id — already at CAS path + verified"
      continue
    fi
    echo "[warn] $id — CAS file exists but SHA mismatch; removing to re-populate"
    rm -f "$cas_path"
  fi

  # Migration path: legacy flat file exists at models/<filename> with
  # the correct SHA. Move it into the CAS layout instead of re-downloading.
  if [[ -f "$legacy_flat_path" ]]; then
    legacy_have=$(shasum -a 256 "$legacy_flat_path" | awk '{print $1}')
    if [[ "$legacy_have" == "$sha256" ]]; then
      mkdir -p "$cas_dir"
      mv "$legacy_flat_path" "$cas_path"
      echo "[migr] $id — moved legacy $filename → $id/${sha256}${ext}"
      continue
    fi
    echo "[warn] $id — legacy $filename present but SHA mismatch; leaving it alone"
  fi

  if [[ "$VERIFY_ONLY" == "1" ]]; then
    echo "[miss] $id — file absent at CAS path (verify-only mode, not downloading)"
    continue
  fi

  echo "[get]  $id — downloading $filename → $cas_path ($size bytes)"
  mkdir -p "$cas_dir"
  curl -fsSL --progress-bar -o "${cas_path}.tmp" "$url"

  got=$(shasum -a 256 "${cas_path}.tmp" | awk '{print $1}')
  if [[ "$got" != "$sha256" ]]; then
    echo "ERROR: $id — SHA-256 mismatch after download." >&2
    echo "       expected: $sha256" >&2
    echo "       got:      $got" >&2
    rm -f "${cas_path}.tmp"
    exit 1
  fi

  mv "${cas_path}.tmp" "$cas_path"
  echo "[ok]   $id — downloaded and verified at $cas_path"
done

echo "Done."
