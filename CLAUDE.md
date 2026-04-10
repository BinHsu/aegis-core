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
