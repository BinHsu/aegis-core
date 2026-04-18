#!/usr/bin/env bash
# tools/scripts/gh_bootstrap.sh
#
# One-shot GitHub-side bootstrap for a fresh Aegis Core clone. Applies
# the configurations that §§2–7 of docs/github-setup.md describe, in
# the order the doc prescribes. Idempotent: every operation re-asserts
# the desired state rather than creating-if-missing, so re-running is
# safe after a partial run.
#
# NOT applied here (the doc calls them out as manual / interactive):
# - §0   repository visibility toggle (destructive, confirmation prompt)
# - §0.5 SSH commit signing key generation (interactive `ssh-keygen`)
# - §1   branch-protection ruleset (depends on §0.5 being done first; full
#         JSON body is in docs/github-setup.md, too long to inline here)
# - §5   CodeQL default setup (Phase 4b scope per ROADMAP.md:314)
# - §7   Discussions category creation (REST API can't create categories)
#
# Usage:
#   ./tools/scripts/gh_bootstrap.sh                    # defaults to BinHsu/aegis-core
#   ./tools/scripts/gh_bootstrap.sh owner/repo         # apply to a fork
#
# Requires: `gh` CLI authenticated with admin scope on the target repo.
# Check via:  gh auth status

set -euo pipefail

REPO="${1:-BinHsu/aegis-core}"

info() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '    \033[0;32m✓\033[0m %s\n' "$*"; }
warn() { printf '    \033[0;33m⚠\033[0m %s\n' "$*" >&2; }

# --- Sanity: gh auth + repo reachability --------------------------------
info "Verifying gh auth + repo access ($REPO)"
gh auth status >/dev/null
gh repo view "$REPO" >/dev/null
ok "gh auth OK + $REPO reachable"

# --- §2 Private vulnerability reporting ---------------------------------
info "§2 Private vulnerability reporting"
gh api --method PUT "repos/$REPO/private-vulnerability-reporting" >/dev/null
ok "private-vulnerability-reporting enabled"

# --- §3 Secret scanning + push protection -------------------------------
# Public repos only — the API returns 403 on private repos without
# GitHub Advanced Security. The doc notes this; we tolerate the 403.
info "§3 Secret scanning + push protection"
if gh api --method PATCH "repos/$REPO" \
    -F "security_and_analysis[secret_scanning][status]=enabled" \
    -F "security_and_analysis[secret_scanning_push_protection][status]=enabled" \
    >/dev/null 2>&1; then
  ok "secret scanning + push protection enabled"
else
  warn "secret scanning PATCH refused — likely a private repo without GHAS. Enable via Settings → Code security and analysis."
fi

# --- §4 Dependabot alerts + security updates ----------------------------
# Version-updates config lives in .github/dependabot.yml (already
# committed); these two endpoints flip on the repo-level features.
info "§4 Dependabot alerts + security updates"
gh api --method PUT "repos/$REPO/vulnerability-alerts" >/dev/null
ok "vulnerability-alerts enabled"
gh api --method PUT "repos/$REPO/automated-security-fixes" >/dev/null
ok "automated-security-fixes enabled"

# --- §6 Actions permissions --------------------------------------------
info "§6 Actions permissions (selected, verified + GitHub-owned)"
gh api --method PUT "repos/$REPO/actions/permissions" \
  -F enabled=true \
  -f "allowed_actions=selected" >/dev/null
gh api --method PUT "repos/$REPO/actions/permissions/selected-actions" \
  -F "github_owned_allowed=true" \
  -F "verified_allowed=true" >/dev/null
ok "Actions limited to GitHub-owned + verified"

gh api --method PUT "repos/$REPO/actions/permissions/access" \
  -f "access_level=none" >/dev/null
ok "outside-collaborator fork-PR workflows require approval"

gh api --method PUT "repos/$REPO/actions/permissions/workflow" \
  -F "default_workflow_permissions=read" \
  -F "can_approve_pull_request_reviews=false" >/dev/null
ok "default GITHUB_TOKEN scope = read-only; cannot approve PRs"

# --- §7 Discussions -----------------------------------------------------
info "§7 Discussions feature"
gh api --method PATCH "repos/$REPO" \
  -F "has_discussions=true" >/dev/null
ok "Discussions feature enabled (create categories via UI)"

# --- Summary ------------------------------------------------------------
cat <<EOF

\033[1;32mBootstrap complete for $REPO.\033[0m

Manual follow-ups from docs/github-setup.md still required:
  - §0.5 SSH commit signing key (interactive; one-time per machine)
  - §1   Branch-protection ruleset (JSON body in the doc; gh api PUT)
  - §7   Discussion categories (UI only — REST cannot create them)
  - §5   CodeQL default setup (Phase 4b scope; enable when scanning lands)

Verify:
  gh api repos/$REPO/actions/permissions
  gh api repos/$REPO/vulnerability-alerts          # HTTP 204 = on
  gh api repos/$REPO --jq '.security_and_analysis'
EOF
