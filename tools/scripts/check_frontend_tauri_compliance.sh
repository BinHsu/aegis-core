#!/usr/bin/env bash
# Aegis Core — ADR-0002 Phase 3 Web Frontend Tauri-compatibility gate.
#
# Each constraint from ADR-0002 §"Constraints on the Phase 3 Web
# Frontend" gets a specific grep that fails the check if the pattern
# appears in `frontend_web/src/` OUTSIDE the provider directory that
# is explicitly allowed to use the underlying browser API.
#
# Constraints covered:
#   1. No `chrome.*` namespace                → grep chrome.
#   2. Audio capture behind AudioCaptureProvider — only that dir may
#      touch `navigator.mediaDevices`         → grep navigator.mediaDevices
#   3. No Service Worker for core             → grep navigator.serviceWorker
#   4. No SharedArrayBuffer                   → grep SharedArrayBuffer
#   5. Filesystem / notification / auto-update behind their providers —
#      the FileSystemProvider / NotificationProvider / AutoUpdateProvider
#      dirs are the only places that may use Blob-download / Notification /
#      updater APIs respectively. Spot-checked: notifications outside
#      NotificationProvider dir may not call `new Notification(...)`.
#   6. No large-client-side-storage assumption — this check is
#      informational only (hard to detect via grep without false
#      positives on in-memory object usage); the pattern warns on any
#      `IndexedDB` reference in the source so a reviewer can confirm
#      it's the small-object tier.
#
# Runs in under a second. Called from pre-commit (frontend hunk) and
# in CI. Exits non-zero on any violation.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
SRC="$REPO_ROOT/frontend_web/src"

if [[ ! -d "$SRC" ]]; then
  echo "error: $SRC does not exist" >&2
  exit 1
fi

VIOLATIONS=0

# ---- Helper: grep and report violation ----
# Args: <rule-name> <pattern> [exclude-path-fragment]
#
# Skips comment lines. The heuristic: after grep's `filename:lineno:`
# prefix, if the line content starts with optional whitespace followed
# by `//`, `*`, or `/*`, it's a single-line comment or a block-comment
# line and we ignore it. This catches the common case where an ADR
# reference legitimately mentions a forbidden API name inside a JSDoc
# block (e.g. "we do NOT call navigator.serviceWorker"). Multi-line
# block comments where the forbidden name appears on a non-`*`-prefixed
# line are not handled — flag-a-false-positive and refactor the comment
# if that ever happens.
check() {
  local rule="$1"
  local pattern="$2"
  local exclude="${3:-}"
  local matches
  # -R recursive, -I skip binary, -n line numbers, -E extended regex.
  # Type set via --include=*.ts --include=*.tsx so we don't scan
  # generated proto stubs in src/gen/ — those come from upstream proto
  # codegen and cannot be audited line-by-line.
  local comment_filter='[^:]*:[^:]*:[[:space:]]*(//|\*|/\*)'
  if [[ -n "$exclude" ]]; then
    matches="$(grep -RInE --include='*.ts' --include='*.tsx' \
      --exclude-dir='gen' \
      "$pattern" "$SRC" \
      | grep -v -E "$comment_filter" \
      | grep -v "$exclude" || true)"
  else
    matches="$(grep -RInE --include='*.ts' --include='*.tsx' \
      --exclude-dir='gen' \
      "$pattern" "$SRC" \
      | grep -v -E "$comment_filter" || true)"
  fi
  if [[ -n "$matches" ]]; then
    echo "✗ ADR-0002 $rule violated:"
    echo "$matches" | sed 's/^/    /'
    VIOLATIONS=$((VIOLATIONS + 1))
  fi
}

echo "== ADR-0002 Phase 3 Web Frontend compliance check =="
echo "   source: frontend_web/src/  (excluding src/gen/)"
echo

# Constraint 1 — chrome.* namespace.
# Excludes `chrome.runtime.*` comment mentions; pattern is the
# characteristic `chrome.` call-site shape: an identifier `chrome`
# followed by a property access, at the start of a statement or after
# a `(`, `=`, `!`, `&`, `|`, `?`, `,`, space. This rules out string
# literals mentioning "chrome.runtime" in comments.
check "Constraint 1 (no chrome.* namespace)" \
  '(^|[^A-Za-z0-9_.])chrome\.[a-zA-Z]'

# Constraint 2 — navigator.mediaDevices may only appear inside the
# AudioCaptureProvider directory.
check "Constraint 2 (navigator.mediaDevices only in AudioCaptureProvider)" \
  'navigator\.mediaDevices' \
  '/AudioCaptureProvider/'

# Constraint 3 — no Service Worker registration. `navigator.serviceWorker`
# is the access surface; we allow zero uses outside of the one spot
# that might legitimately register (none today).
check "Constraint 3 (no Service Worker for core)" \
  'navigator\.serviceWorker'

# Constraint 4 — no SharedArrayBuffer references.
check "Constraint 4 (no SharedArrayBuffer)" \
  'SharedArrayBuffer'

# Constraint 5a — notifications only via NotificationProvider.
check "Constraint 5 (notifications only via NotificationProvider)" \
  'new Notification\(' \
  '/NotificationProvider/'

# Constraint 5b — downloads only via FileSystemProvider.
# `a.download = ...` / `anchor.download = ...` is the characteristic
# Blob-download trick; a less formal grep on `URL.createObjectURL` is
# too noisy (audio streams legitimately use it). We look for the
# anchor-download idiom specifically.
check "Constraint 5 (Blob downloads only via FileSystemProvider)" \
  '\.download = ' \
  '/FileSystemProvider/'

# Constraint 6 — informational warning only; IndexedDB is permitted
# but large-binary storage is not. Flag for reviewer attention.
INDEXEDDB="$(grep -RInE --include='*.ts' --include='*.tsx' \
  --exclude-dir='gen' \
  'indexedDB\.open|IDBDatabase' "$SRC" || true)"
if [[ -n "$INDEXEDDB" ]]; then
  echo "ℹ  Constraint 6 note — IndexedDB references found. Reviewer"
  echo "   should confirm these store small JSON only, not model"
  echo "   blobs / audio. References:"
  echo "$INDEXEDDB" | sed 's/^/    /'
fi

echo

if [[ "$VIOLATIONS" -gt 0 ]]; then
  echo "FAIL — $VIOLATIONS constraint(s) violated."
  echo "See docs/adr/0002-desktop-shell-technology.md §\"Constraints"
  echo "on the Phase 3 Web Frontend\" for the full rules."
  exit 1
fi

echo "OK — all ADR-0002 Phase 3 Constraints are satisfied."
