<!--
  Aegis Core PR template.
  Match the shape: Summary → Files → Test plan. Conventional Commits
  scope goes in the PR title (e.g. `feat(host): …`, `fix(gateway): …`,
  `docs(adr): …`); commit message scopes are enforced by the
  `--force-scope` pre-commit hook, so the title should line up.
  Delete any section that doesn't apply rather than leaving it blank.
-->

## Summary

<!--
  1-3 sentences on the *why* — what problem this PR solves or what
  capability it unlocks. Not "what it does" (the diff shows that).
  Link ADRs, ARCH §s, ROADMAP items, issues.
-->

## Files

<!--
  Optional but load-bearing for multi-file PRs. Call out new files
  and non-obvious edits. Skip if the diff is small and self-explanatory.
-->

## Test plan

<!--
  Checkbox list. Lean on the 8-job CI matrix; add manual verification
  steps only where CI cannot prove the thing.
-->

- [ ] `./tools/bazelisk/bazelisk test //…` (or scoped targets) green
- [ ] `./tools/scripts/frontend.sh typecheck && build` green (if frontend touched)
- [ ] `./tools/scripts/check_frontend_tauri_compliance.sh` green (if frontend touched)
- [ ] `./tools/scripts/frontend.sh e2e` green (if host UI touched)
- [ ] 8-job CI matrix green before admin-merge

## Notes

<!--
  Anything a reviewer should know that isn't obvious from the diff:
  rollback plan for risky change, follow-up issue filed, deferred
  work, incident postmortem linked, etc.
-->
