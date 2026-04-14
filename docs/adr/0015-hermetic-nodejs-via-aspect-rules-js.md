# ADR-0015: Hermetic Node.js via `aspect_rules_js`

| Field    | Value                                                                       |
| -------- | --------------------------------------------------------------------------- |
| Status   | Accepted                                                                    |
| Date     | 2026-04-14                                                                  |
| Deciders | Project author                                                              |
| Context  | Phase 3 frontend kickoff requires installing npm dependencies; the machine has no system `node`/`npm`; CLAUDE.md Rule 6 mandates hermetic toolchains. |
| Related  | ADR-0013 (proto codegen distribution), CLAUDE.md Rule 6 (hermetic toolchain), ROADMAP Phase 3 scope |

## Context

Phase 1 C1–C4 scaffolded `frontend_web/` with React + Vite + TypeScript
under the working assumption that `frontend_web/` would use local
`npm` inside that directory only, with `rules_nodejs` integration
"deferred to Phase 4a" per CLAUDE.md Rule 6. That compromise was
acceptable during Phase 1–2 when the frontend had no runtime
dependencies beyond React/Vite dev tools.

Phase 3 changes the calculation:

1. **First NPM dependencies land.** `@bufbuild/protobuf`,
   `@connectrpc/connect-web`, `oidc-client-ts`, and `qrcode.react`
   are required for the Host-UI build-out. These aren't dev tools;
   they ship in the bundle.
2. **The portfolio-grade promise is on the line.** CLAUDE.md Rule 3
   says "Anyone downloading this must be able to compile and run it
   locally with minimal struggle." A recruiter cloning the repo
   cannot realistically be expected to `brew install node@20` as a
   prerequisite — that exposes the seam the rest of the build system
   works so hard to hide.
3. **The dev machine has no system Node.** Discovered while wiring
   up `npm install` for Phase 3 deps. This forces the decision now
   rather than letting it drift to Phase 4a.
4. **aspect_rules_js has matured.** The Aspect-maintained fork of
   `rules_nodejs` handles the hard cases (pnpm lockfile translation,
   hermetic Node toolchain, IDE-compatible `node_modules` linking)
   in a way the original `rules_nodejs` struggled with during Phase
   1 scoping.

## Decision

**Adopt `aspect_rules_js` now (Phase 3) for hermetic Node.js + pnpm
toolchain management**, earlier than the Phase 4a date originally
planned.

Concretely:

1. `MODULE.bazel` adds `bazel_dep(name = "aspect_rules_js", …)` and
   `bazel_dep(name = "aspect_rules_ts", …)`.
2. A hermetic Node 20 LTS toolchain is registered via the
   `aspect_rules_js` node extension — Bazel downloads Node into
   `.bazel_cache/` alongside the existing hermetic Go SDK, clang, and
   `buf` binaries.
3. pnpm (bundled with `aspect_rules_js`) replaces npm for dependency
   management. `pnpm-lock.yaml` lives at the repo root and is
   committed.
4. `frontend_web/package.json` declares the app's deps; a
   `pnpm-workspace.yaml` at the repo root makes `frontend_web/` a
   workspace member (future packages land as additional workspace
   members without re-architecting).
5. `frontend_web/BUILD.bazel` uses
   `npm_link_all_packages(name = "node_modules")` so the Bazel
   `node_modules/` symlink tree is IDE-visible (TypeScript and
   Vite's file watcher pick it up).
6. A thin wrapper `tools/scripts/frontend.sh {dev|build|typecheck}`
   exists for developer ergonomics — under the hood it invokes the
   Bazel-managed pnpm / node, so the wrapper and the Bazel target
   graph use the same toolchain.

## Consequences

### Positive

- **CLAUDE.md Rule 6 fully honored.** No `brew install node`
  prerequisite; cloning a fresh repo and running
  `./tools/bazelisk/bazelisk run //:app_local` in Phase 3+ is a
  single command with no host-OS install steps.
- **Single source of dependency truth.** `pnpm-lock.yaml` is
  authoritative; `package.json` declares ranges; Bazel reads both.
  The frontend joins the rest of the repo's hermetic discipline.
- **Phase 4a OCI packaging unblocked.** When we package the frontend
  bundle into an OCI image (ROADMAP Phase 4a), the build graph
  already has a `js_binary` / `vite_build` node to attach to. No
  retroactive plumbing.
- **Portfolio signal.** "Hermetic polyglot build including frontend
  Node.js" is the kind of platform-engineer fluency that reads
  clearly in a technical round.

### Negative / costs

- **MODULE.bazel grows.** Another set of `bazel_dep` entries,
  another extension invocation, another `use_repo` block. Manageable
  but not free.
- **pnpm, not npm.** A recruiter familiar with `npm install` will
  see `./tools/scripts/frontend.sh install`  instead. We document
  this in the README and the wrapper messages so the transition is
  clear.
- **First-run build cost.** Bazel now downloads Node (~35 MB) on
  first invocation of any frontend target. Amortized over the
  session; no effect on subsequent builds.
- **Vite-specific rules are still evolving.**
  `aspect_rules_js` has first-class `js_run_devserver` but
  `vite build` under full Bazel sandbox still requires care (Vite's
  plugin ecosystem assumes un-sandboxed filesystem in some cases).
  Mitigation: the wrapper script runs Vite outside sandbox for the
  inner dev loop; full hermetic `bazel build //frontend_web:bundle`
  target arrives in Phase 4a when it's actually needed for
  production packaging.

## Alternatives Considered

### A. Install Node.js via Homebrew; keep local `npm` in `frontend_web/`

- **Pros:** Zero Bazel changes. Developer workflow is familiar.
- **Cons:** Violates CLAUDE.md Rule 6 literally. Developer is
  expected to pre-install Node before the repo is usable, contradicting
  the "clone, build, just works" promise this project markets.
- **Rejected** because the "clone, build, works" promise is a
  portfolio-grade commitment; slipping on it here undermines the
  rest of the repo's hermetic story.

### B. Use the original `rules_nodejs` (not Aspect's fork)

- **Pros:** More widely documented; bigger existing community.
- **Cons:** Original rules_nodejs has known pain points with
  pnpm support, peer dependencies, and IDE integration (the linked
  `node_modules` tree isn't always where TypeScript expects it).
  Aspect's fork specifically addresses these via
  `npm_translate_lock` and `npm_link_all_packages`.
- **Rejected** because the specific workflow we need (pnpm
  lockfile, modern Vite project, IDE-visible node_modules) is exactly
  what Aspect's fork was built to solve.

### C. Run everything via a pre-made Docker container

- **Pros:** Completely isolates the Node toolchain.
- **Cons:** Adds Docker as a build-time prerequisite (another host
  install); complicates the inner dev loop (Vite HMR inside Docker
  is messy); doesn't actually make the *rest* of the repo more
  hermetic — just adds a second hermetic layer on top.
- **Rejected** because Docker is a downstream packaging concern
  (Phase 4a OCI images), not a day-to-day build tool here.

### D. Wait for Phase 4a as originally planned

- **Pros:** Avoids touching MODULE.bazel mid-Phase-3.
- **Cons:** Would either require (a) forcing the dev machine to
  install Node via brew for Phase 3 work (the very thing we're
  trying to avoid) or (b) blocking all Phase 3 frontend work until
  Phase 4a. Neither is acceptable.
- **Rejected** because the timing pressure forcing the decision
  is structural, not incidental.

## Implementation checklist

- [x] `MODULE.bazel`: added `aspect_rules_js` 2.1.2, `aspect_rules_ts`
      3.2.0, `rules_nodejs` 6.3.0, node-toolchain extension pinned at
      20.11.1, pnpm extension, `npm_translate_lock` extension +
      `use_repo`.
- [x] Root `package.json` (workspace declaration only) +
      `pnpm-workspace.yaml` listing `frontend_web`.
- [x] Bootstrapped `pnpm-lock.yaml` via the Bazel-managed pnpm.
      Committed alongside this change.
- [x] `.npmrc` at repo root pins `store-dir=.pnpm-store` so pnpm's
      content-addressable store stays inside the repo (discovered
      during bootstrap that pnpm's default picks an out-of-repo
      location — Rule 6 violation in motion).
- [x] `tools/scripts/frontend.sh {install|dev|build|typecheck}`
      wrapper resolves the Bazel-managed Node binary and sets
      `BAZEL_BINDIR="."` so the aspect_rules_js js_binary pnpm
      wrapper runs cleanly outside a Bazel action.
- [x] `frontend_web/vite.config.ts` — added `resolve.alias` mirror
      of tsconfig `paths` (Vite does not read tsconfig; would fail
      `@/…` imports at build time otherwise).
- [x] `frontend_web/tsconfig.json` — dropped `erasableSyntaxOnly`
      (TS 5.8+ option, we're on 5.7); added `types: ["vite/client"]`
      for `import.meta.env` typing.
- [x] Fixed the existing Phase 1 C1 code's React 19 compat — all
      three JSX.Element references now `import type { JSX } from "react"`
      because React 19 dropped the global JSX namespace.
- [x] `.gitignore`: `node_modules/` + `.pnpm-store/` + friends
      remain ignored; explicit comment in the Node section now
      references this ADR.
- [x] `README.md`: Quick Start now references
      `./tools/scripts/frontend.sh {install|dev|build|typecheck}`.
- [x] `CLAUDE.md` Rule 6: Node.js line updated from "Phase 3+ will
      use rules_nodejs; until then local npm" to the realized
      "ALWAYS via tools/scripts/frontend.sh" statement.

## Verification

After this ADR lands, a fresh clone goes:

```bash
git clone …
./tools/scripts/download_models.sh --all          # one-time model fetch
./tools/scripts/frontend.sh install                 # one-time npm install
./tools/scripts/frontend.sh typecheck               # passes
./tools/scripts/frontend.sh build                   # produces dist/ bundle
```

No host `node` / `npm` / `pnpm` required at any point. The first
`frontend.sh` invocation downloads the hermetic Node 20.11.1 (~35 MB)
into `.bazel_cache/`; subsequent invocations are cache hits.
