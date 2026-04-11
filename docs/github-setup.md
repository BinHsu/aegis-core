# GitHub Repository Setup (Admin-only)

This document lists GitHub-side configurations that **cannot be applied
from the repository filesystem** and must be clicked into the GitHub
web UI by a repository administrator. It complements `.github/CODEOWNERS`,
`.github/workflows/`, and `.pre-commit-config.yaml` — those files cover
the repo-side enforcement; this document covers the rest.

**Apply these during Phase 0 setup**, before the first external
contributor joins.

---

## 1. Branch Protection on `main`

Navigate to **Settings → Branches → Add branch protection rule** for
`main` and set:

### Required

- [ ] **Require a pull request before merging**
  - [ ] Require approvals: **1** (minimum for MVP; raise to 2 when team
    grows)
  - [ ] Dismiss stale pull request approvals when new commits are
    pushed
  - [ ] Require review from Code Owners
- [ ] **Require status checks to pass before merging**
  - [ ] Require branches to be up to date before merging
  - [ ] Required checks (add once they have run at least once on
    `main`):
    - [ ] `Pre-commit hooks`
    - [ ] `Gitleaks secret scan`
    - [ ] `Proto lint` (once proto files exist)
    - [ ] `Markdown link check`
- [ ] **Require conversation resolution before merging**
- [ ] **Require signed commits** (GPG or SSH)
- [ ] **Require linear history** — no merge commits on `main`; use
  squash or rebase
- [ ] **Do not allow bypassing the above settings**

### Optional but recommended

- [ ] Require deployments to succeed before merging (enable when
  staging environment exists, Phase 4+)
- [ ] Lock branch (only used for release branches post-v1.0)

### Disallowed

- [ ] **Allow force pushes**: OFF
- [ ] **Allow deletions**: OFF

**Rationale**: enforces CLAUDE.md Rule 1 (honesty via signed commits),
CLAUDE.md Rule 2 (test integrity via required status checks), and
protects the architecture-critical files from unreviewed changes.

---

## 2. Private Vulnerability Reporting

Navigate to **Settings → Code security and analysis** and enable:

- [ ] **Private vulnerability reporting** — allows security researchers
  to privately report vulnerabilities per the process documented in
  `SECURITY.md`.

Once enabled, the "Security" tab on the repository will show a "Report
a vulnerability" button.

---

## 3. GitHub Secret Scanning

Navigate to **Settings → Code security and analysis**:

- [ ] **Secret scanning** — enabled for the repository.
- [ ] **Push protection** — enabled. Blocks commits containing detected
  secrets before they reach the remote.

**Rationale**: belt-and-braces with `gitleaks` in pre-commit hooks (see
`.pre-commit-config.yaml`) and in CI (`.github/workflows/ci-baseline.yml`).
Push protection catches anything that gets past the local hook.

---

## 4. Dependabot and Dependency Review

- [ ] **Dependency graph**: enabled (should be on by default for public
  repos).
- [ ] **Dependabot alerts**: enabled.
- [ ] **Dependabot security updates**: enabled — auto-PR for known
  vulnerable dependencies.
- [ ] **Dependabot version updates**: configured via `.github/dependabot.yml`
  (TODO Phase 1 once dependencies exist).

---

## 5. Code Scanning (CodeQL)

CodeQL runs on schedule and is language-aware. Enable when Phase 1
components ship:

- Navigate to **Settings → Code security and analysis → Code scanning**
- [ ] Set up CodeQL analysis
- [ ] Languages to scan: `c-cpp`, `go`, `javascript-typescript`
- [ ] Schedule: weekly, and on every PR touching the relevant language

**Note**: CodeQL requires a non-trivial baseline of code to scan. Enable
after Phase 1 Bazel targets exist.

---

## 6. Actions Permissions

Navigate to **Settings → Actions → General**:

- [ ] **Actions permissions**: Allow enterprise, and select non-enterprise,
  actions and reusable workflows.
- [ ] **Allow actions created by GitHub**: ON
- [ ] **Allow actions from verified creators**: ON
- [ ] **Allow specified actions**: pin any third-party actions to a
  specific SHA rather than a tag. The workflows in
  `.github/workflows/ci-baseline.yml` already do this implicitly via
  major-version tags; pin to SHAs before enabling third-party PRs.
- [ ] **Fork pull request workflows from outside collaborators**:
  *Require approval for all outside collaborators* — prevents drive-by
  PRs from exfiltrating secrets via modified workflows.
- [ ] **Workflow permissions**: *Read repository contents and packages
  permissions*. Grant write only to workflows that explicitly need it.

---

## 7. Discussions

Enable **Discussions** for community Q&A:

- [ ] Navigate to **Settings → General → Features → Discussions**: ON
- [ ] Create categories: `Announcements`, `Q&A`, `Ideas`, `Show and
  Tell`, `Security` (linked to SECURITY.md).

---

## 8. Issue Templates and PR Template

These live in `.github/ISSUE_TEMPLATE/` and `.github/PULL_REQUEST_TEMPLATE.md`
(TODO Phase 0+ — not blocking Phase 0 completion).

---

## Verification Checklist

After applying all of the above, verify with:

```
gh api repos/<owner>/aegis-core/branches/main/protection
gh api repos/<owner>/aegis-core/private-vulnerability-reporting
gh api repos/<owner>/aegis-core/vulnerability-alerts
```

Expected responses confirm the settings above are active.

---

## Related

- `CLAUDE.md` — ironclad rules enforced by the above controls
- `SECURITY.md` — the vulnerability disclosure process these settings
  support
- `.github/CODEOWNERS` — review requirements enforced via branch
  protection's "Require review from Code Owners" setting
- `.github/workflows/ci-baseline.yml` — required status checks
- `.pre-commit-config.yaml` — local enforcement counterpart
