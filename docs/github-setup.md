# GitHub Repository Setup (Admin-only)

This document lists GitHub-side configurations that must be applied by
a repository administrator. Most steps have a **`gh` CLI command**
(faster, scriptable, reproducible) and a **UI fallback** (when `gh`
doesn't support the operation, or for visual confirmation).

**Prerequisites for `gh` commands**:

```bash
gh auth status                    # verify you're logged in
gh auth refresh -s admin:repo_hook,write:repo_hook,delete_repo  # if needed
```

The commands below assume the repo is `BinHsu/aegis-core`. Replace as
needed. Run them from any directory; `gh` works repo-agnostic.

**Apply these during Phase 0 setup**, before the first external
contributor joins.

---

## 0. Repository Visibility

If the repo is currently private and you want to use rulesets /
advanced security features on a personal (non-org) GitHub account:

```bash
# Make the repo public (required for rulesets on free personal accounts)
gh repo edit BinHsu/aegis-core --visibility public
```

**Note**: visibility change is irreversible via `gh` without
confirmation. If you change your mind, you can flip back via UI.

---

## 1. Branch Protection (Ruleset) on `main`

The legacy "Branch Protection Rules" have been superseded by
**Rulesets** in 2024+. Use rulesets for new repos.

### `gh` CLI

```bash
# Create a ruleset that requires PR, linear history, and blocks
# deletes/force-pushes on main.
gh api --method POST repos/BinHsu/aegis-core/rulesets \
  -f name="main" \
  -f target="branch" \
  -f enforcement="active" \
  -f "conditions[ref_name][include][]=refs/heads/main" \
  -f "rules[][type]=deletion" \
  -f "rules[][type]=non_fast_forward" \
  -f "rules[][type]=required_linear_history" \
  -f "rules[][type]=pull_request" \
  -F "rules[-1][parameters][required_approving_review_count]=1" \
  -F "rules[-1][parameters][dismiss_stale_reviews_on_push]=true" \
  -F "rules[-1][parameters][require_code_owner_review]=true" \
  -F "rules[-1][parameters][required_review_thread_resolution]=true"

# Add the repo admin (yourself) to the bypass list so you can push
# directly to main during bootstrap and hotfixes.
# Replace <USER_ID> with your numeric GitHub user ID
# (get it via: gh api user --jq .id).
YOUR_ID=$(gh api user --jq .id)
RULESET_ID=$(gh api repos/BinHsu/aegis-core/rulesets --jq '.[] | select(.name=="main") | .id')

gh api --method PUT "repos/BinHsu/aegis-core/rulesets/$RULESET_ID" \
  -f "bypass_actors[][actor_id]=5"            `# 5 = Repository admin role` \
  -f "bypass_actors[-1][actor_type]=RepositoryRole" \
  -f "bypass_actors[-1][bypass_mode]=always"
```

### UI fallback

Navigate to **Settings → Rules → New branch ruleset**:

- **Ruleset Name**: `main`
- **Enforcement status**: Active
- **Target branches**: add target, `Include by pattern` = `main`
- **Rules** (check each):
  - ✅ Restrict deletions
  - ✅ Require linear history
  - ✅ Require a pull request before merging
    - Required approvals: 1
    - ✅ Dismiss stale pull request approvals when new commits are pushed
    - ✅ Require review from Code Owners
    - ✅ Require conversation resolution before merging
  - ✅ Block force pushes
  - ⬜ Require signed commits — **only if you have GPG/SSH commit signing configured**
  - ⬜ Require status checks to pass — add **after** CI has run at least once
  - ⬜ (everything else stays unchecked)
- **Bypass list**: add `Repository admin` role (so owner can push during bootstrap)
- Save

### Later: add required status checks

After the first `git push origin main` succeeds and CI runs at least
once, the check names are registered with GitHub. Only then can they
be added to the ruleset.

**`gh` PUT replaces the entire ruleset**, so prepare a JSON payload
with the existing rules PLUS the new `required_status_checks` rule:

```bash
REPO=BinHsu/aegis-core
RULESET_ID=$(gh api repos/$REPO/rulesets --jq '.[] | select(.name=="main") | .id')

# Fetch current config for reference
gh api repos/$REPO/rulesets/$RULESET_ID --jq '{name, target, enforcement, bypass_actors, conditions, rules}'

# Build update payload — keep all existing rules, append required_status_checks
cat > /tmp/ruleset_update.json <<'JSON'
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [
    {"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}
  ],
  "conditions": {
    "ref_name": {
      "include": ["refs/heads/main"],
      "exclude": []
    }
  },
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {"type": "required_linear_history"},
    {
      "type": "pull_request",
      "parameters": {
        "allowed_merge_methods": ["squash"],
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": true,
        "require_last_push_approval": false,
        "required_approving_review_count": 1,
        "required_review_thread_resolution": true
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          {"context": "Pre-commit hooks",      "integration_id": 15368},
          {"context": "Gitleaks secret scan",  "integration_id": 15368},
          {"context": "Proto lint",            "integration_id": 15368},
          {"context": "Markdown link check",   "integration_id": 15368}
        ]
      }
    }
  ]
}
JSON

gh api --method PUT "repos/$REPO/rulesets/$RULESET_ID" --input /tmp/ruleset_update.json \
  --jq '{name, enforcement, rules: [.rules[] | .type]}'
```

**Note**: `integration_id: 15368` is GitHub Actions. If you add
checks from a different provider, its integration_id differs —
find it via `gh api /users/<app>/apps` or the Status Checks API.

### UI fallback

Edit the ruleset → check "Require status checks to pass to merge"
→ in the search box, type each check name (CI must have run at
least once for names to be searchable).

### Verify

```bash
gh api repos/BinHsu/aegis-core/rulesets --jq '.[] | {id, name, enforcement}'
gh api repos/BinHsu/aegis-core/rulesets/$RULESET_ID --jq '.rules'
```

### Caveat: Account type

> "Your rulesets won't be enforced on this private repository until
> you move to GitHub Team organization account."

On free personal accounts, rulesets enforce only on **public** repos.
If the repo is private, either make it public (§0 above), upgrade to
GitHub Pro, or move to a GitHub Team org.

---

## 2. Private Vulnerability Reporting

### `gh` CLI

```bash
gh api --method PUT repos/BinHsu/aegis-core/private-vulnerability-reporting
```

### UI fallback

**Settings → Code security and analysis → Private vulnerability
reporting → Enable**.

### Verify

```bash
gh api repos/BinHsu/aegis-core/private-vulnerability-reporting --jq .enabled
# expected: true
```

---

## 3. GitHub Secret Scanning

### `gh` CLI

```bash
gh api --method PATCH repos/BinHsu/aegis-core \
  -F "security_and_analysis[secret_scanning][status]=enabled" \
  -F "security_and_analysis[secret_scanning_push_protection][status]=enabled"
```

### UI fallback

**Settings → Code security and analysis**:

- ✅ Secret scanning → Enable
- ✅ Push protection → Enable (appears after secret scanning is on)

### Verify

```bash
gh api repos/BinHsu/aegis-core --jq '.security_and_analysis'
```

---

## 4. Dependabot

### `gh` CLI

```bash
# Dependabot alerts
gh api --method PUT repos/BinHsu/aegis-core/vulnerability-alerts

# Dependabot security updates (auto-PR for vulnerable deps)
gh api --method PUT repos/BinHsu/aegis-core/automated-security-fixes
```

### UI fallback

**Settings → Code security and analysis**:

- ✅ Dependency graph (usually on by default for public repos)
- ✅ Dependabot alerts → Enable
- ✅ Dependabot security updates → Enable
- ⬜ Dependabot version updates — configured via
  `.github/dependabot.yml` (TODO Phase 1 when dependencies exist)

### Verify

```bash
gh api repos/BinHsu/aegis-core/vulnerability-alerts
# HTTP 204 if enabled
```

---

## 5. Code Scanning (CodeQL)

**Defer to Phase 1+** — CodeQL needs a non-trivial code baseline to
scan. Enable when Bazel targets ship.

### `gh` CLI (Phase 1+)

```bash
gh api --method PUT repos/BinHsu/aegis-core/code-scanning/default-setup \
  -F state=configured \
  -f "languages[]=c-cpp" \
  -f "languages[]=go" \
  -f "languages[]=javascript-typescript" \
  -f "query_suite=default"
```

### UI fallback

**Settings → Code security and analysis → Code scanning → Set up
CodeQL analysis**.

---

## 6. Actions Permissions

### `gh` CLI

```bash
# Limit Actions to GitHub-verified and selected actions only.
gh api --method PUT repos/BinHsu/aegis-core/actions/permissions \
  -F enabled=true \
  -f "allowed_actions=selected"

gh api --method PUT repos/BinHsu/aegis-core/actions/permissions/selected-actions \
  -F "github_owned_allowed=true" \
  -F "verified_allowed=true"

# Require approval for PRs from outside collaborators.
gh api --method PUT repos/BinHsu/aegis-core/actions/permissions/access \
  -f "access_level=none"

# Default workflow permissions: read-only.
gh api --method PUT repos/BinHsu/aegis-core/actions/permissions/workflow \
  -F "default_workflow_permissions=read" \
  -F "can_approve_pull_request_reviews=false"
```

### UI fallback

**Settings → Actions → General**:

- Actions permissions: "Allow enterprise, and select non-enterprise"
- ✅ Allow actions created by GitHub
- ✅ Allow actions from verified creators
- Fork pull request workflows: "Require approval for all outside collaborators"
- Workflow permissions: "Read repository contents and packages permissions"

---

## 7. Discussions

### `gh` CLI

```bash
gh api --method PATCH repos/BinHsu/aegis-core \
  -F "has_discussions=true"
```

Categories must be created via UI or GraphQL (REST API does not
support category creation).

### UI fallback

**Settings → General → Features → Discussions**: ON. Then navigate to
**Discussions tab → Categories** and create:

- `Announcements`, `Q&A`, `Ideas`, `Show and Tell`, `Security` (linked
  to SECURITY.md)

---

## 8. Issue Templates and PR Template

File-based, not UI. Create `.github/ISSUE_TEMPLATE/*.yml` and
`.github/PULL_REQUEST_TEMPLATE.md`. TODO Phase 0+ — not blocking
Phase 0 completion.

---

## One-shot bootstrap script

For fresh repo setup, the Phase 0 maintainer can run:

```bash
./tools/scripts/gh_bootstrap.sh BinHsu/aegis-core
```

(Script TODO Phase 0+ — lives at `tools/scripts/gh_bootstrap.sh`
when created. Until then, run the `gh` commands above in order.)

---

## Full Verification

After applying all of the above:

```bash
REPO=BinHsu/aegis-core
echo "=== Ruleset ==="
gh api repos/$REPO/rulesets --jq '.[] | {name, enforcement, target}'
echo "=== Private vuln reporting ==="
gh api repos/$REPO/private-vulnerability-reporting --jq .enabled
echo "=== Secret scanning ==="
gh api repos/$REPO --jq '.security_and_analysis.secret_scanning.status, .security_and_analysis.secret_scanning_push_protection.status'
echo "=== Vulnerability alerts ==="
gh api repos/$REPO/vulnerability-alerts --include 2>&1 | head -1
echo "=== Visibility ==="
gh api repos/$REPO --jq .visibility
echo "=== Discussions ==="
gh api repos/$REPO --jq .has_discussions
```

Expected output:

```
=== Ruleset ===
{"name":"main","enforcement":"active","target":"branch"}
=== Private vuln reporting ===
true
=== Secret scanning ===
enabled
enabled
=== Vulnerability alerts ===
HTTP/2.0 204 No Content
=== Visibility ===
public
=== Discussions ===
true
```

---

## Related

- `CLAUDE.md` — ironclad rules enforced by the above controls
- `SECURITY.md` — the vulnerability disclosure process these settings
  support
- `.github/CODEOWNERS` — review requirements enforced via ruleset's
  "Require review from Code Owners"
- `.github/workflows/ci-baseline.yml` — required status checks
- `.pre-commit-config.yaml` — local enforcement counterpart
