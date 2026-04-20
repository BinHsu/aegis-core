#!/usr/bin/env bash
# Aegis Core — manifest / engine drift check.
#
# Guardrail for the discipline declared in models/manifest.schema.json:
#     `required: true` iff some engine startup code path unconditionally
#     loads this model.
#
# Checks:
#   (1) Every `required: true` entry's `id` must appear in at least one
#       of the engine startup source files (main.cc, session.cc) — proxy
#       for "engine actually loads this at startup."
#   (2) PLACEHOLDER entries (sha256 starts with "PLACEHOLDER" OR filename
#       starts with "PLACEHOLDER") must be `required: false` — can't
#       require a model we haven't picked yet.
#
# Exits non-zero on drift. Intended for pre-commit + CI.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
MANIFEST="$REPO_ROOT/models/manifest.json"

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required." >&2
  exit 1
fi

if [[ ! -f "$MANIFEST" ]]; then
  echo "ERROR: $MANIFEST not found" >&2
  exit 1
fi

# Engine startup sources that the walker draws `required: true` entries from.
# Keep this list small — adding new entries is an intentional escalation
# of what counts as "engine startup."
ENGINE_STARTUP_SOURCES=(
  "$REPO_ROOT/engine_cpp/cmd/engine/main.cc"
  "$REPO_ROOT/engine_cpp/src/session/session.cc"
)

violations=0

# Check (1): required=true entries must appear as string literals in one
# of the engine startup sources, OR be resolvable by type through the
# walker contract. We check the id AND the type — either hit counts as
# "engine loads it." For type, we check whether main.cc iterates on
# `type == "<type>"` (the walker's type-based selection).
while read -r entry; do
  id=$(jq -r '.id' <<<"$entry")
  type=$(jq -r '.type' <<<"$entry")

  matched=0
  for src in "${ENGINE_STARTUP_SOURCES[@]}"; do
    if [[ ! -f "$src" ]]; then continue; fi
    # Direct id literal in code.
    if grep -qF "\"$id\"" "$src"; then
      matched=1
      break
    fi
    # Type-based selection — walker picks by `type == "xxx"`.
    if grep -qF "type == \"$type\"" "$src"; then
      matched=1
      break
    fi
  done

  if [[ "$matched" == "0" ]]; then
    echo "DRIFT: manifest entry id=\"$id\" is required=true but" \
         "neither its id nor its type=\"$type\" appears in any" \
         "engine startup source:" >&2
    for src in "${ENGINE_STARTUP_SOURCES[@]}"; do
      echo "         $src" >&2
    done
    echo "       → Either wire engine startup to load this model, OR" \
         "demote to required=false with a note explaining why." >&2
    violations=$((violations + 1))
  fi
done < <(jq -c '.models[] | select(.required == true)' "$MANIFEST")

# Check (2): PLACEHOLDER entries cannot be required=true.
while read -r entry; do
  id=$(jq -r '.id' <<<"$entry")
  sha=$(jq -r '.sha256' <<<"$entry")
  filename=$(jq -r '.filename' <<<"$entry")
  required=$(jq -r '.required' <<<"$entry")

  is_placeholder=0
  if [[ "$sha" == PLACEHOLDER* || "$filename" == PLACEHOLDER* ]]; then
    is_placeholder=1
  fi

  if [[ "$is_placeholder" == "1" && "$required" == "true" ]]; then
    echo "DRIFT: manifest entry id=\"$id\" has PLACEHOLDER metadata" \
         "(sha=$sha, filename=$filename) but is marked required=true." >&2
    echo "       Cannot require a model that has not been pinned." >&2
    echo "       → Demote to required=false + notes explaining" \
         "re-promotion conditions." >&2
    violations=$((violations + 1))
  fi
done < <(jq -c '.models[]' "$MANIFEST")

if [[ "$violations" -gt 0 ]]; then
  echo "" >&2
  echo "manifest drift check failed: $violations violation(s)" >&2
  exit 1
fi

echo "manifest drift check: OK ($(jq '.models | length' "$MANIFEST") entries,"\
     "$(jq '[.models[] | select(.required == true)] | length' "$MANIFEST") required)"
