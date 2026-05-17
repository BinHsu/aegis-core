# 🤖 AI Agent Directives (CLAUDE.md)

**WARNING TO ALL AI AGENTS:** You are operating in a multi-turn, multi-agent environment where context is passed between runs. You MUST adhere to these ironclad rules to avoid catastrophic system divergence.

## The Ironclad Engineering Rules

1. **Self-Awareness & Honesty**
   * Do NOT guess. If you do not know how to implement something, explicitly say "I do not know."
   * Do NOT hallucinate progress. If you did not execute a step, explicitly say you did not execute it.
   * If you notice your token output limits approaching, STOP your work securely and warn the user. Do not rush, do not cut corners, do not lie to finish early.
   * When verifying CI status, inspect **EVERY job** in the workflow run, not just the one you happened to be watching or the one that usually matters (e.g., the Bazel build job). A single failing job turns the whole run red — claiming "CI green" based on one job's log when another job silently failed is hallucinating progress. Check `gh run view <id>` for the full job matrix, or explicitly grep for both passing and failing signals (`✓` AND `X`) before reporting status to the user.

2. **Testing Integrity**
   * Code must have legitimate tests. Do NOT write stub tests that just `return true` or `assert(1 == 1)` to get a green light. Real inputs must produce verifiable real outputs.
   * **Test-first, and tests MUST follow Boundary Value Analysis (BVA) — non-negotiable.** Every test suite for an input domain with a meaningful boundary `B` MUST cover `B-1`, `B`, and `B+1` — the classic three-point BVA pattern. Equivalence-class-only tests (one happy-path value per class) are insufficient: the off-by-one bugs that hurt most are exactly at the boundary, and a single in-class value will not catch them. Boundaries include: numeric thresholds (timeouts, payload sizes, retry counts, rate limits), index/length limits (off-by-one in slices, loops, ring buffers), state transitions (session count == max, queue full/empty, capacity exhausted), and temporal cliffs (token expiry, deadline, debounce window). When a boundary is genuinely untestable at the UT layer (e.g., real wall-clock expiry, kernel-level cliff), name it explicitly per the escape-hatch clause below — silence is not a pass. PRs that ship only interior-of-class assertions WILL be bounced.
   * **Test-first commit discipline.** For any bug that a unit or integration test *could have caught*, write the regression test **in the same commit / PR as the fix**, and verify the test is actually load-bearing (it fails on the pre-fix code, passes on the post-fix code). Do NOT commit the fix alone on the promise of a follow-up test — follow-ups decay, and the next regression of the same shape lands unnoticed. Incident 14 (2026-04-20 LAN transcript three-layer bug) is the canonical case: the `WebSocketTranscriptStreamProvider` dropping binary frames was a 10-line Vitest away from being caught in Phase 1; not having it cost hours.
   * **Testing escape hatches must be named explicitly.** Some bugs are genuinely beyond UT scope — cross-process protocol timing (gRPC keepalive policy, HTTP/2 GOAWAY cadence), OS-level behaviors, hardware-dependent code paths. When declining to write a UT for a specific bug, the PR body MUST say so AND explain what layer *could* catch it (e.g., "long-running staging canary"). Silent omission is how Rule 2 rots over time.
   * **Local `bazel test` MUST be green before `git commit`.** Pre-commit hooks cover lint and format; they do NOT run full test suites. The author is responsible for `./tools/bazelisk/bazelisk test //...` (or the relevant scoped subset) passing locally before the commit, not merely before the PR merge. "CI will catch it" is a valid safety net, not a valid substitute for the discipline.

3. **Mandatory Documentation Synchronization**
   * Before writing code, you MUST read `ARCHITECTURE.md` and `ROADMAP.md`.
   * After completing a task, you MUST update `ROADMAP.md` (to check off progress) and `ARCHITECTURE.md` (if a systemic decision was made).
   * Update `README.md` if any user-facing steps (like local execution commands) change. The core philosophy of this project is identical to V1: **"Anyone downloading this must be able to compile and run it locally with minimal struggle."**
   * **Before ending any session, discover + update every `session-close-review`-marked doc.** These files self-register via a `<!-- session-close-review: <axis> -->` HTML comment near their top, which states the axis that needs re-verification (status, narrative, trust-surface, incidents, etc.). Discover the full set with:

     ```bash
     grep -rIln "session-close-review:" . --include='*.md'
     ```

     For each hit, re-read the declared axis and confirm the doc still reflects reality after this session's commits. Common axes today: `README.md` Status table + narrative, `docs/threat-model.md` trust-surface list, `docs/incidents.md` postmortem entries per Rule 7. The list grows by adding markers to new docs — CLAUDE.md does NOT enumerate the filenames, so there is no hardcoded list to drift out of date.
   * Also run a cheap placeholder-drift check before closing:

     ```bash
     grep -rIn "TODO\|WIP\|coming soon\|Slice [0-9]\+ — TODO" . --include='*.md'
     ```

     Hits mean some doc carries a promise the session just made real — resolve in place rather than letting the stale language linger.
   * ROADMAP.md checklist updates are necessary but not sufficient; the marker-discovered docs are the public-facing face and have to keep up.

4. **Tech Boundaries (Enforce System Architecture)**
   * Frontend: TypeScript/React/Svelte, Tauri (Rust).
   * Gateway: Go.
   * Core Engine: C++.
   * Communication: gRPC, gRPC-Web, WebRTC.
   * Do NOT introduce massive frameworks outside this scope without explicit architectural discussion and ADR documentation.

5. **Language Conventions**
   * **ALL generated code, comments, commit messages, file names, and project documentation (like `.md` files) MUST be written strictly in English.** This ensures global open-source compatibility.
   * Multilingual or local languages (like Traditional Chinese) should ONLY be used during conversational interactions/chat with the human user.

6. **Strict Directory Confinement — All Toolchains Are Hermetic**
   * **The foundational premise of this project is: clone it, build it, it just works — with zero reliance on anything the host OS happens to have installed.** Every compiler, runtime, SDK, and tool is managed inside the repository. Do NOT assume any system-provided binary is present or correct.
   * **ABSOLUTE RULE**: Every action, dependency, cache, and model MUST be strictly confined to the current repository directory. Do NOT step out of bounds.
   * If a user clones this repo into `D://temp`, you do not touch `C://` or `~/.cache` under any circumstances. **DO NOT pollute the user's global system directories.**
   * **The correct entry point for ALL build and test operations is `./tools/bazelisk/bazelisk`.** Bazel manages every hermetic toolchain in this repo:
     - **Go** — SDK 1.24.12 via `go_sdk.download`; NEVER run `go`, `gofmt`, or `go test` directly.
     - **C++** — hermetic clang/LLVM toolchain; NEVER run `clang++`, `cmake`, or `make` directly.
     - **Protobuf / gRPC** — `buf` via pre-commit; codegen via Bazel `proto_library` rules; NEVER run `protoc` directly.
     - **Node.js / TypeScript** — hermetic Node 20 LTS + pnpm via `aspect_rules_js` (ADR-0015). ALWAYS invoke via `./tools/scripts/frontend.sh {install|dev|build|typecheck}`; NEVER run a system `node` / `npm` / `pnpm`. `pnpm-lock.yaml` at the repo root is authoritative; the content-addressable store lives at `./.pnpm-store/` per `.npmrc`.
     - **Python** — if ever needed, use `.venv` inside the repo; NEVER install packages globally.
   * Big models (`.gguf`/`.ggml`) must be downloaded to `/models` inside the repo; NEVER to `~/.cache` or system model directories.
   * Bazel itself MUST be configured via `.bazelrc` with `--output_user_root=./.bazel_cache`.
   * **Bazel test flag reminder**: `--test_output=short` is NOT valid; use `summary`, `errors`, `all`, or `streamed`.

7. **Incident Postmortems (Field Notes Discipline)**
   * When you encounter a **non-trivial development-time blocker** — working definition: ≥ 15 minutes of debugging, OR two or more failed fix attempts, OR a root cause more than one layer below the surface error — you MUST add a postmortem entry to `docs/incidents.md` before the task is considered done.
   * Use the existing template verbatim: `Symptom → Root cause → Detection → Resolution → Prevention → Lessons`. Severity scale is defined at the top of `docs/incidents.md` (S2/S3/S4/S5 development-time scale; do NOT invent new levels).
   * Be honest about **red herrings and failed attempts**. "We first tried X which didn't work because Y" is load-bearing for the lesson; omitting it turns the postmortem into marketing.
   * Link the resolving commit hash; keep nitty-gritty details (full error text, full diff) in the commit message, keep the **narrative layer** in the postmortem.
   * Trivial bugs (typo in a BUILD file fixed in 60 seconds, clang-format whitespace) do NOT warrant a postmortem — don't pollute the file.
   * This rule is a **learning-culture signal**. Treat it with the same seriousness as Rule 2 Testing Integrity.

8. **Root-Cause Fixes Over Workarounds**
   * When something fails (CI, build, test, merge), **investigate the actual root cause and fix it**. Do NOT bypass with flags like `--admin`, `--no-verify`, or skip logic.
   * Workarounds are acceptable ONLY when the effort to fix properly is disproportionately high (e.g., requires upstream changes, multi-day refactor). In that case, present BOTH the proper fix AND the workaround with effort estimates, and let the human decide.
   * The default is always "fix it right." Discuss the tradeoff with the human before acting — never silently choose the shortcut.

9. **Environment Pre-flight (Trust the Handbook, Verify the Machine)**
   * Before you start committing work on a fresh clone, run a one-shot pre-flight check: confirm the repo's own tooling is wired up for THIS machine. Specifically:
     - `ls .git/hooks/pre-commit .git/hooks/commit-msg` — both must exist. If not, run `pre-commit install && pre-commit install --hook-type commit-msg` exactly as `CONTRIBUTING.md` §Development Setup mandates.
     - `git config --get user.signingkey && git config --get commit.gpgsign` — signing must be live. If not, follow `docs/github-setup.md` §0.5.
     - `./tools/bazelisk/bazelisk --version` — bazelisk wrapper must be on PATH from the repo root. If the first invocation has to download Bazel, that is expected.
   * The handbook (`CONTRIBUTING.md`, `docs/github-setup.md`) is the source of truth for these steps. The risk pattern to avoid: **"the docs said X, I skipped it, then I asked the user why my commits keep getting rewritten by clang-format in CI."** That is drift by omission, not drift by documentation error.
   * If you discover the pre-flight is missing a check that would have caught a real mistake in the current task, **add the check here** as part of closing that task.

10. **Main Agent vs Subagent: A Decision, Not a Default**
    * **Rule:** The main conversation thread is the human's point of contact — it drives dialog, decisions, and edits. Delegate to subagents only when delegation is **net-cheaper** than inline execution.
    * **Delegate when:**
      - Output is a summary / answer (human won't read the raw tool output).
      - Scope is wide: > 5 files, cross-directory scans, multi-round grep.
      - Work is independent of the next conversational turn (use `run_in_background: true`).
      - Investigation is pure recon with no downstream edit dependency.
    * **Stay inline when:**
      - Fewer than ~5 tool calls total.
      - Raw content will be quoted, edited, or referenced verbatim.
      - Result feeds directly into the next edit (no parallelism gain).
      - Human is watching and wants to see each step.
    * **Signal you mis-delegated:** subagent returns a summary but you have to re-read the files anyway to make the edit. Next time: inline.
    * **Signal you mis-inlined:** main thread hit ~30% context on tool output before you even started the real work. Next time: delegate.

11. **Naming Convention — Full Repo Name, Never a Bare `aegis-` Prefix**
    * When you name anything owned by *one specific repository* — a container image, an IAM role, a Kubernetes object, a metric namespace, a CI workflow id, an OIDC subject claim — the prefix MUST be the **full repository name**: `aegis-core-…`, `aegis-aws-landing-zone-…`. Do NOT shorten it to a bare `aegis-` prefix.
    * The bare `aegis-` prefix is reserved for resources genuinely shared by the **entire environment** — an AWS account, an org-wide DNS zone, a shared VPC. If a name does not span every repo, it does not earn the bare prefix.
    * Rationale: `aegis-gateway` reads as "the gateway of the whole Aegis world", but it is really "aegis-core's gateway". The bare prefix hides the owning repo and collides the instant a second repo ships a similarly-named component; it also makes IAM policies, Grafana dashboards, and cross-repo issues ambiguous about who owns what. `aegis-core-gateway` is unambiguous.
    * This is a **forward discipline** — name every new resource correctly from creation. The bare-prefixed names this repo historically shipped (the `aegis-gateway` / `aegis-engine` OCI images, the `aegis_gateway_*` / `aegis_engine_*` metric families, the gateway/engine Kubernetes objects) were migrated to the full `aegis-core-` prefix in the Rule 11 rename PR — `aegis-core-gateway` / `aegis-core-engine` images, `aegis_core_gateway_*` / `aegis_core_engine_*` metrics. The aegis-aws-landing-zone side (ECR repository name, ServiceMonitor scrape discovery, Grafana dashboard/alert queries, the engine IRSA trust-policy subject) is tracked as a cross-repo rebind. A future bare-prefixed name is a rule violation, not a pending migration.
