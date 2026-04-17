# Runbook — BuildBuddy Personal Cache Onboarding

| Field | Value |
| --- | --- |
| Audience | **Upstream repo operator** (required); **fork operator** (optional, if fork wants its own cache namespace) |
| Applies to | ADR-0014 Option β (Phase A — demo horizon) |
| Not applicable to | Casual cloners. Local `bazel build` is fully hermetic; no cloud signup required. |
| Estimated time | 5–10 minutes end-to-end |
| Cost | $0 — BuildBuddy Personal tier (100 GB/month cache transfer, up to 80 remote-build cores) |
| Last reviewed | 2026-04-17 |

## Purpose

Wire BuildBuddy's managed Bazel remote cache into the upstream
`aegis-core` CI pipeline. This accelerates cold CI runs on
`.github/workflows/ci-baseline.yml` by reusing build artifacts
across PRs. Without this, cold CI runs take ~15 minutes; with it,
cache hits compile in seconds.

This runbook is the **prerequisite for the β wiring PR** — the PR
that adds `--remote_cache=grpcs://remote.buildbuddy.io` to the CI
workflow will fail on every run until a valid
`BUILDBUDDY_API_KEY` secret exists in the repo.

## Prerequisites

- A GitHub account that owns (or has admin access to) the
  `aegis-core` repository. For upstream, this is `BinHsu`.
- A browser session signed into that GitHub account.

Nothing else — no CLI, no AWS, no Terraform.

## Step 1 — Sign up for BuildBuddy Personal

1. Open <https://app.buildbuddy.io/>.
2. Click **"Sign in with GitHub"**.
3. Authorize the BuildBuddy OAuth app when GitHub prompts for it.
   BuildBuddy requests read-only access to your public profile and
   email; it does NOT need repo or code-write scopes.
4. On the first-time setup screen, choose or create a **personal
   organization**. The default name is your GitHub username — keep
   it unless you have a reason to rename.
5. Confirm you are on the **Personal (Free)** tier — the dashboard
   header should NOT show a "Team" / "Enterprise" badge.

After this step you have an empty BuildBuddy org dashboard at
`https://app.buildbuddy.io/` but no API keys yet.

## Step 2 — Create an org-scoped API key

1. Open <https://app.buildbuddy.io/settings/org/api-keys> directly.
   (Alternative click path: from the dashboard, the gear icon in
   the left sidebar → **Org settings** → **API keys** tab.)
2. Click **"Create new API key"** (button label may also read
   **"New API key"** depending on BuildBuddy UI version).
3. Fill in the form:
   - **Label**: `aegis-core-ci` — descriptive; shows up in
     BuildBuddy's audit log next to each request. Do not use a
     personal identifier.
   - **Capabilities**: leave defaults (**cache read + write**).
     Do NOT check "Read-only" — CI must be able to upload to the
     cache to populate it across PRs.
     Do NOT check "Executor" — that's for self-hosted executor
     nodes; we are not running any.
4. Click **"Create"**.
5. The generated API key appears on screen **once**. It will look
   like `BB_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx`.
   **Copy the entire string to your clipboard immediately** —
   BuildBuddy does not re-display the key after you navigate away.
   If you lose it, delete the key in the UI and create a new one.

Do not commit, Slack, email, or paste the key anywhere other than
GitHub Actions secrets (next step).

## Step 3 — Add the key as a GitHub Actions secret

1. Open
   <https://github.com/BinHsu/aegis-core/settings/secrets/actions>
   directly. (Alternative click path: on the `aegis-core` repo
   page → **Settings** tab → left sidebar under **Security** →
   **Secrets and variables** → **Actions** → the **Secrets** tab.)
2. Click **"New repository secret"**.
3. Fill in the form:
   - **Name**: `BUILDBUDDY_API_KEY` — exactly this string, case
     sensitive. The CI workflow references it by this name; a
     typo (`BUILD_BUDDY_API_KEY`, `BUILDBUDDY_APIKEY`, etc.) will
     silently produce a blank header and every cache request will
     401.
   - **Secret**: paste the entire key from Step 2, with no leading
     or trailing whitespace. If you copy-pasted from BuildBuddy's
     UI, double-check the value for a stray trailing newline
     (GitHub Actions will strip it, but better to be explicit —
     paste, then press End, then backspace any blank line).
4. Click **"Add secret"**.
5. The secret is now listed under "Repository secrets" with value
   redacted. GitHub does NOT allow retrieving the value again;
   you can only update or delete it.

For fork operators: the URL in the click path above becomes
`https://github.com/<your-username>/aegis-core/settings/...`.

## Verification

After the β wiring PR lands and merges, the first CI run on any
subsequent PR should show:

- In BuildBuddy's dashboard at
  <https://app.buildbuddy.io/history/>, a new invocation entry
  tagged with the repo, commit SHA, and a non-zero cache hit count.
- In the GitHub Actions log for that run, lines like:
  ```
  INFO: Streaming build results to: https://app.buildbuddy.io/invocation/<uuid>
  ```
  and an absence of lines matching
  `remote cache: UNAUTHENTICATED` or `remote cache: PERMISSION_DENIED`.

If neither shows up, the key or secret name is wrong. Start from
Step 3.

## Rotation

BuildBuddy Personal keys do not expire on a fixed cadence. Rotate
manually:

- **Every 180 days** (routine hygiene).
- **Immediately** on any suspected exposure (key leaked in a log,
  former contributor needs revocation, key committed by accident).

Rotation procedure (zero-downtime):

1. Create a **new** API key in BuildBuddy with a fresh label like
   `aegis-core-ci-2026Q3`.
2. Update the `BUILDBUDDY_API_KEY` GitHub Actions secret to the
   new key's value (GitHub's "Update secret" button in the same
   secrets page). The next CI run will use it.
3. Let one CI run complete green to confirm the new key works.
4. In BuildBuddy, delete the **old** key.

## Revocation (emergency)

If the key is known-compromised:

1. In <https://app.buildbuddy.io/settings/org/api-keys>, click the
   old key's **"Delete"** / trash icon. The key is invalidated
   server-side immediately; any in-flight CI runs start receiving
   `UNAUTHENTICATED` on the next cache request.
2. Create a new key (Step 2 above).
3. Update the GHA secret (Step 3 above).
4. If the leak reached public code (pushed commit, public Slack,
   etc.), also rotate any BuildBuddy org-level secrets or
   credentials that the same compromise path might have reached.

## Related

- [ADR-0014 Bazel Build Cache Strategy](../adr/0014-bazel-build-cache-strategy.md)
  — why Option β was chosen for Phase A, and the β→δ migration
  plan for Phase B (production S3 + OIDC).
- [CONTRIBUTING.md §Remote cache](../../CONTRIBUTING.md#remote-cache-optional-ci-only)
  — fork-posture guide for someone pointing CI at their own cache
  infra.
- [`.github/workflows/ci-baseline.yml`](../../.github/workflows/ci-baseline.yml)
  — the workflow that consumes `BUILDBUDDY_API_KEY`.
