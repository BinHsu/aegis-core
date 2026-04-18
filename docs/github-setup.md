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

## 0.5. SSH Commit Signing Setup (macOS)

Required before enabling `required_signatures` in the ruleset (§1).
GitHub supports both GPG and SSH commit signing; SSH is simpler
(no keyring, no expiration management, same key can be used for auth).

These steps were tested on macOS with the system ssh-agent +
Keychain integration. For Linux, replace `--apple-use-keychain` with
`ssh-agent` + optional `keychain(1)`.

### Step 1 — Create `~/.ssh/` and set git identity

Fully automatable via `gh`:

```bash
mkdir -p ~/.ssh && chmod 700 ~/.ssh

# Use the GitHub-provided no-reply email for privacy (doesn't leak
# your real email into public git history). Format:
#   <user_id>+<login>@users.noreply.github.com
USER_ID=$(gh api user --jq .id)
LOGIN=$(gh api user --jq .login)
NOREPLY="${USER_ID}+${LOGIN}@users.noreply.github.com"

git config --global user.name  "$LOGIN"
git config --global user.email "$NOREPLY"
```

### Step 2 — Generate the signing key (interactive)

Run in your shell. You'll be prompted twice for a passphrase:

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519 -C "<login> signing key (YYYY-MM)"
```

- Use a real passphrase — macOS Keychain will cache it so you don't
  have to retype it per commit.
- ed25519 is the GitHub-recommended algorithm.

### Step 3 — Configure ssh-agent with Keychain (macOS only)

Automatable:

```bash
cat > ~/.ssh/config <<'EOF'
Host *
  AddKeysToAgent yes
  UseKeychain yes
  IdentityFile ~/.ssh/id_ed25519
EOF
chmod 600 ~/.ssh/config
```

Then **interactively** (shell) add the key to the agent/Keychain:

```bash
ssh-add --apple-use-keychain ~/.ssh/id_ed25519
```

- Prompts for passphrase once; stored in Keychain thereafter.
- Verify: `ssh-add -l` should list the key fingerprint.

### Step 4 — Configure git for SSH signing

Fully automatable:

```bash
# Create the allowed_signers file (needed for local verify-commit)
PUBKEY=$(cat ~/.ssh/id_ed25519.pub)
EMAIL=$(git config --global user.email)
echo "$EMAIL $PUBKEY" > ~/.ssh/allowed_signers
chmod 644 ~/.ssh/allowed_signers

git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
git config --global tag.gpgsign true
git config --global gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers
```

### Step 5 — Upload the public key to GitHub as a signing key

The default `gh auth` token does not have the `write:ssh_signing_key`
scope. Refresh it first **interactively** (shell):

```bash
gh auth refresh -s write:ssh_signing_key
```

- Displays a one-time code; opens the browser to
  `https://github.com/login/device` where you paste the code.
- After success, verify: `gh auth status` should list
  `write:ssh_signing_key` among token scopes.

Then upload the public key (automatable):

```bash
gh api --method POST user/ssh_signing_keys \
  -f title="$(whoami)@$(hostname -s) ($(date +%Y-%m))" \
  -f key="$(cat ~/.ssh/id_ed25519.pub)" \
  --jq '{id, title, created_at}'
```

### Step 6 — Verify

Create an empty signed commit and check GitHub accepts it:

```bash
cd <your-repo>
git commit --allow-empty -m "chore: verify SSH commit signing"
git log -1 --show-signature   # expect: Good "git" signature ...

git push origin main          # or your branch
sleep 3
gh api repos/<owner>/<repo>/commits/HEAD --jq \
  '{sha, verified: .commit.verification.verified, reason: .commit.verification.reason}'
# expected: {"sha":"...", "verified": true, "reason": "valid"}
```

If `verified: false`, common reasons:

- `bad_email` — `user.email` in git does not match the email
  associated with the signing key on GitHub. The no-reply email
  from Step 1 is auto-verified and works.
- `unsigned` — `commit.gpgsign true` not set, or ssh-agent doesn't
  have the key loaded (re-run `ssh-add --apple-use-keychain ...`).
- `no_user` — the signing key isn't uploaded to GitHub yet, or the
  wrong key fingerprint.

### Step 7 — Enable `required_signatures` in the ruleset

Once signing works locally, flip the ruleset's
`required_signatures` rule on. See §1 "Ruleset with signing
requirement" below for the full PUT payload.

### UI fallback

For Steps 1–6, there is no meaningful UI path — SSH signing setup is
inherently shell-based. For Step 5 public key upload specifically,
the UI alternative is **Settings → SSH and GPG keys → New SSH key**
with **Key type = Signing Key** (not Authentication Key).

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
          {"context": "Pre-commit hooks",            "integration_id": 15368},
          {"context": "Gitleaks secret scan",        "integration_id": 15368},
          {"context": "Proto lint",                  "integration_id": 15368},
          {"context": "Markdown link check",         "integration_id": 15368},
          {"context": "Proto codegen drift check",   "integration_id": 15368},
          {"context": "Bazel unit tests",            "integration_id": 15368}
        ]
      }
    }
  ]
}
JSON

gh api --method PUT "repos/$REPO/rulesets/$RULESET_ID" --input /tmp/ruleset_update.json \
  --jq '{name, enforcement, rules: [.rules[] | .type]}'
```

### Ruleset with signing requirement (after §0.5 SSH signing is set up)

Once SSH signing is confirmed working (see §0.5), add
`{"type": "required_signatures"}` to the `rules` array:

```bash
# Same payload as above, but with required_signatures rule added.
# Edit /tmp/ruleset_update.json to insert:
#   {"type": "required_signatures"}
# anywhere in the "rules" array, then:
gh api --method PUT "repos/$REPO/rulesets/$RULESET_ID" --input /tmp/ruleset_update.json \
  --jq '{enforcement, rules: [.rules[] | .type]}'
# expected: {..., "required_signatures", ...} present
```

**Do NOT enable `required_signatures` before every contributor has
set up signing** — unsigned pushes will be rejected. For
bootstrap/solo development, the repo-admin bypass in the ruleset
allows pushing unsigned commits from the admin account.

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
- ✅ Dependabot version updates — configured via
  [`.github/dependabot.yml`](../.github/dependabot.yml) (five
  ecosystems: github-actions, gomod, npm, bazel, plus a commented-out
  Docker stanza waiting for Phase 4a)

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

File-based, not UI.

- ✅ PR template — [`.github/PULL_REQUEST_TEMPLATE.md`](../.github/PULL_REQUEST_TEMPLATE.md)
  — matches the Summary / Files / Test plan shape the session PRs
  already use, with the 8-job CI matrix called out as the default
  gate.
- ⬜ Issue templates — `.github/ISSUE_TEMPLATE/*.yml`. Deferred until
  external contributors start opening issues; the current single-
  maintainer flow doesn't benefit from them.

---

## One-shot bootstrap script

For fresh repo setup, the Phase 0 maintainer can run:

```bash
./tools/scripts/gh_bootstrap.sh                 # defaults to BinHsu/aegis-core
./tools/scripts/gh_bootstrap.sh owner/repo      # for a fork
```

The script asserts §§2, 3, 4, 6, and 7 idempotently. It deliberately
does NOT apply §0 (destructive visibility toggle), §0.5 (interactive
`ssh-keygen`), §1 (long ruleset JSON — apply by hand), §5 (CodeQL —
Phase 4b scope per ROADMAP), or §7 category creation (REST API does
not support it).

---

## Full Verification

After applying all of the above:

```bash
REPO=BinHsu/aegis-core
echo "=== Ruleset rules ==="
gh api repos/$REPO/rulesets --jq '.[] | {name, enforcement, target}'
gh api repos/$REPO/rulesets/$(gh api repos/$REPO/rulesets --jq '.[] | select(.name=="main") | .id') \
  --jq '[.rules[] | .type]'
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
echo "=== HEAD commit signature ==="
gh api repos/$REPO/commits/HEAD --jq '.commit.verification | {verified, reason}'
echo "=== My SSH signing keys on GitHub ==="
gh api user/ssh_signing_keys --jq '.[] | {id, title, created_at}'
```

Expected output:

```
=== Ruleset rules ===
{"name":"main","enforcement":"active","target":"branch"}
["deletion","non_fast_forward","pull_request","required_linear_history","required_status_checks","required_signatures"]
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
=== HEAD commit signature ===
{"verified":true,"reason":"valid"}
=== My SSH signing keys on GitHub ===
{"id":896375,"title":"BinHsu MacBook Air (2026-04)",...}
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
