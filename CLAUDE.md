# 🤖 AI Agent Directives (CLAUDE.md)

**WARNING TO ALL AI AGENTS:** You are operating in a multi-turn, multi-agent environment where context is passed between runs. You MUST adhere to these ironclad rules to avoid catastrophic system divergence.

## The Ironclad Engineering Rules

1. **Self-Awareness & Honesty**
   * Do NOT guess. If you do not know how to implement something, explicitly say "I do not know."
   * Do NOT hallucinate progress. If you did not execute a step, explicitly say you did not execute it.
   * If you notice your token output limits approaching, STOP your work securely and warn the user. Do not rush, do not cut corners, do not lie to finish early.

2. **Testing Integrity**
   * Code must have legitimate tests. Do NOT write stub tests that just `return true` or `assert(1 == 1)` to get a green light. Real inputs must produce verifiable real outputs.

3. **Mandatory Documentation Synchronization**
   * Before writing code, you MUST read `ARCHITECTURE.md` and `ROADMAP.md`.
   * After completing a task, you MUST update `ROADMAP.md` (to check off progress) and `ARCHITECTURE.md` (if a systemic decision was made).
   * Update `README.md` if any user-facing steps (like local execution commands) change. The core philosophy of this project is identical to V1: **"Anyone downloading this must be able to compile and run it locally with minimal struggle."**

4. **Tech Boundaries (Enforce System Architecture)**
   * Frontend: TypeScript/React/Svelte, Tauri (Rust).
   * Gateway: Go.
   * Core Engine: C++.
   * Communication: gRPC, gRPC-Web, WebRTC.
   * Do NOT introduce massive frameworks outside this scope without explicit architectural discussion and ADR documentation.

5. **Language Conventions**
   * **ALL generated code, comments, commit messages, file names, and project documentation (like `.md` files) MUST be written strictly in English.** This ensures global open-source compatibility.
   * Multilingual or local languages (like Traditional Chinese) should ONLY be used during conversational interactions/chat with the human user.

6. **Strict Directory Confinement (The Repo Boundary)**
   * **ABSOLUTE RULE**: Every single action, dependency, cache, and model MUST be strictly confined to the current repository directory. Do NOT step out of bounds.
   * If a user clones this repo into `D://temp`, you do not touch `C://` or `~/.cache` under any circumstances. **DO NOT pollute the user's global system directories.**
   * **Mandatory Isolation**: You MUST use virtual environments (`.venv` for Python), local `node_modules`, and isolated workspaces to prevent ANY side effects on the host OS.
   * Big models (.ggml files) must be downloaded strongly to a local `/models` directory within the repo.
   * Build tools must be scoped locally. For example, Bazel MUST be configured via `.bazelrc` to set `--output_user_root=./.bazel_cache`.

7. **Incident Postmortems (Field Notes Discipline)**
   * When you encounter a **non-trivial development-time blocker** — working definition: ≥ 15 minutes of debugging, OR two or more failed fix attempts, OR a root cause more than one layer below the surface error — you MUST add a postmortem entry to `docs/incidents.md` before the task is considered done.
   * Use the existing template verbatim: `Symptom → Root cause → Detection → Resolution → Prevention → Lessons`. Severity scale is defined at the top of `docs/incidents.md` (S2/S3/S4/S5 development-time scale; do NOT invent new levels).
   * Be honest about **red herrings and failed attempts**. "We first tried X which didn't work because Y" is load-bearing for the lesson; omitting it turns the postmortem into marketing.
   * Link the resolving commit hash; keep nitty-gritty details (full error text, full diff) in the commit message, keep the **narrative layer** in the postmortem.
   * Trivial bugs (typo in a BUILD file fixed in 60 seconds, clang-format whitespace) do NOT warrant a postmortem — don't pollute the file.
   * This rule is a portfolio-grade **learning-culture signal**. Treat it with the same seriousness as Rule 2 Testing Integrity.

8. **Environment Pre-flight (Trust the Handbook, Verify the Machine)**
   * Before you start committing work on a fresh clone, run a one-shot pre-flight check: confirm the repo's own tooling is wired up for THIS machine. Specifically:
     - `ls .git/hooks/pre-commit .git/hooks/commit-msg` — both must exist. If not, run `pre-commit install && pre-commit install --hook-type commit-msg` exactly as `CONTRIBUTING.md` §Development Setup mandates.
     - `git config --get user.signingkey && git config --get commit.gpgsign` — signing must be live. If not, follow `docs/github-setup.md` §0.5.
     - `./tools/bazelisk/bazelisk --version` — bazelisk wrapper must be on PATH from the repo root. If the first invocation has to download Bazel, that is expected.
   * The handbook (`CONTRIBUTING.md`, `docs/github-setup.md`) is the source of truth for these steps. The risk pattern to avoid: **"the docs said X, I skipped it, then I asked the user why my commits keep getting rewritten by clang-format in CI."** That is drift by omission, not drift by documentation error.
   * If you discover the pre-flight is missing a check that would have caught a real mistake in the current task, **add the check here** as part of closing that task.
