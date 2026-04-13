#!/usr/bin/env bash
# Aegis Core — model download + SHA-256 verification.
#
# Reads models/manifest.json, downloads each model's file from origin_url
# into models/, and verifies the SHA-256 matches the manifest. Idempotent:
# already-correct files are skipped. Per ARCH §10.1 Supply Chain Integrity.
#
# Usage:
#   ./tools/scripts/download_models.sh                 # all required=true models
#   ./tools/scripts/download_models.sh --model <id>    # one model by id
#   ./tools/scripts/download_models.sh --verify-only   # no download, just check
#   ./tools/scripts/download_models.sh --all           # include optional (required=false) models
#
# The engine model loader (Session 4c+) re-verifies SHA-256 at mmap time;
# this script is the pre-flight that saves the user a failed engine boot.

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
      sed -n '3,19p' "$0" | sed 's/^# \{0,1\}//'
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

# Process each matching model entry.
jq -c "$FILTER" "$MANIFEST" | while read -r entry; do
  id=$(jq -r '.id' <<<"$entry")
  filename=$(jq -r '.filename' <<<"$entry")
  sha256=$(jq -r '.sha256' <<<"$entry")
  size=$(jq -r '.size_bytes' <<<"$entry")
  url=$(jq -r '.origin_url' <<<"$entry")
  dest="$MODELS_DIR/$filename"

  # Skip entries with placeholder metadata (PHASE 1 partial population).
  if [[ "$sha256" == PLACEHOLDER* || "$filename" == PLACEHOLDER* ]]; then
    echo "[skip] $id — manifest entry still has PLACEHOLDER fields"
    continue
  fi

  # If file already present AND hash matches → skip.
  if [[ -f "$dest" ]]; then
    have=$(shasum -a 256 "$dest" | awk '{print $1}')
    if [[ "$have" == "$sha256" ]]; then
      echo "[ok]   $id — already present and verified"
      continue
    fi
    echo "[warn] $id — exists but SHA-256 mismatch; re-downloading"
    rm -f "$dest"
  fi

  if [[ "$VERIFY_ONLY" == "1" ]]; then
    echo "[miss] $id — file absent or bad hash (verify-only mode, not downloading)"
    continue
  fi

  echo "[get]  $id — downloading $filename ($size bytes)"
  curl -fsSL --progress-bar -o "$dest.tmp" "$url"

  got=$(shasum -a 256 "$dest.tmp" | awk '{print $1}')
  if [[ "$got" != "$sha256" ]]; then
    echo "ERROR: $id — SHA-256 mismatch after download." >&2
    echo "       expected: $sha256" >&2
    echo "       got:      $got" >&2
    rm -f "$dest.tmp"
    exit 1
  fi

  mv "$dest.tmp" "$dest"
  echo "[ok]   $id — downloaded and verified"
done

echo "Done."
