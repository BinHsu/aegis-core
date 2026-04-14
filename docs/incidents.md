# Incident Postmortems — Aegis Core

Field notes from real debugging during Phase 0–1. These are not
crashes in production (the project is pre-release); they are
**development-time incidents** that blocked progress long enough to
be worth writing down. Each entry follows a standard postmortem
template so the learnings transfer rather than staying buried in
commit messages or Slack threads.

**Severity scale (development / pre-release)**:

- **S2** — All development blocked, no obvious workaround.
- **S3** — One component blocked; debugging path is unclear but
  progress elsewhere is possible.
- **S4** — Degraded or surprising behavior; workaround exists.
- **S5** — Platform / tooling surprise that forces a redirect.

---

## Incident 01 — macOS CLT-only Bazel cascade (whisper.cpp unreachable)

**Date**: 2026-04-13  **Severity**: S2  **Duration**: ~45 min
**Related commits**: `2ab69af`, `d8b4988`

### Symptom

`bazel build //proto/aegis/v1:aegis_cc_grpc` crashed Bazel itself
during the analysis → execution transition:

```
FATAL: bazel crashed due to an internal error.
Caused by: java.lang.IllegalArgumentException:
  com.google.devtools.build.lib.rules.apple.DottedVersion$
  InvalidDottedVersionException: Dotted version components must all
  start with the form \d+([a-z0-9]*?)?(\d+)? but got 'None'
  at com.google.devtools.build.lib.exec.local.XcodeLocalEnvProvider
     .rewriteLocalEnv(XcodeLocalEnvProvider.java:98)
```

### Root cause

**Two independent bugs interacting.** The stack trace pointed only at
the second; the first hid behind it for the first ~20 minutes.

1. **Path with spaces in `--output_user_root`.** The repo lives at
   `/Volumes/Samsung PSSD T7 Media/aegis-core/`. Bazel's bazelisk
   wrapper routed `--output_user_root` to `<repo>/.bazel_cache`
   per CLAUDE.md Rule 6 (no global pollution). Bazel's Darwin
   sandbox subprocess invocation did not quote the path; sandbox
   setup failed in a way that surfaced as a *different* error in
   `XcodeLocalEnvProvider`, not as a "bad path" message.

2. **No full Xcode installed.** This Mac has only the Command Line
   Tools (`xcode-select -p` returns `/Library/Developer/CommandLineTools`).
   Bazel 7.4.1's `XcodeLocalEnvProvider` invokes `xcode-locator`
   which returns the literal string `"None"` on CLT-only systems;
   `DottedVersion.fromString("None")` then throws. Even after fixing
   (1), this crash remained.

### Detection

`bazel shutdown` printed an easily-missed warning on a subsequent run:

```
WARNING: Output user root "/Volumes/Samsung PSSD T7 Media/
aegis-core/.bazel_cache" contains a space. This will probably break
the build.
```

That single line was the pivot — the actual error in the stack trace
was a downstream symptom.

### Resolution

- **Bazelisk wrapper fix** (`tools/bazelisk/bazelisk`): detect
  spaces in `$REPO_ROOT`, route `--output_user_root` to
  `/tmp/aegis-bazel-<sha256-12>` when present. Documented as a
  CLAUDE.md Rule 6 exception.
- **CLT fix** (`.bazelrc`): explicitly set `DEVELOPER_DIR` to the
  CLT path and pin the xcode config to the `@bazel_tools`
  provided one:
  ```
  build --action_env=DEVELOPER_DIR=/Library/Developer/CommandLineTools
  build --xcode_version_config=@bazel_tools//tools/cpp:host_xcodes
  build --macos_sdk_version=15.4
  build --macos_minimum_os=11.0
  ```
- Added `apple_support 1.21.0` bazel_dep because grpc's
  `universal_binary` rule transitively required a target that only
  ships via apple_support in bzlmod.

### Prevention

- Bazelisk wrapper now auto-detects spaces and warns on every
  invocation: `"[bazelisk] repo path contains spaces; using cache
  at /tmp/..."`.
- `.bazelrc` carries explicit inline comments linking to this
  postmortem and to upstream bugs `bazelbuild/bazel#17037`,
  `#21106`.
- ADR-0009's "Upstream whisper.cpp version bump procedure" (§6-step
  SOP) was updated to include a pre-build sanity check on
  `xcode-select -p`.

### Lessons

1. **Stack traces on Bazel / JVM tools are often one layer shallow.**
   `XcodeLocalEnvProvider` crashed parsing "None", but the real
   cause was the path-with-spaces. Always re-read earlier warning
   output before diving into the crash.
2. **Tool output ordering lies.** Bazel prints the space warning on
   a later `shutdown`, not on the first failing build. This is a
   Bazel UX bug worth patching upstream; for us, the lesson is
   "run `bazel shutdown` after weird failures".
3. **CLT-only macOS is a second-class citizen in the Bazel +
   Apple tooling world.** The workaround stack (`DEVELOPER_DIR`,
   `host_xcodes` alias, SDK version overrides, apple_support) is
   non-trivial. Document it early; don't rediscover it.

---

## Incident 02 — boringssl `-Werror` defeats global `--copt=-Wno-error`

**Date**: 2026-04-13  **Severity**: S3  **Duration**: ~15 min
**Related commit**: `b114398`

### Symptom

After adding abseil and boringssl (transitively via grpc) to the
engine binary, the build failed mid-compile:

```
external/boringssl~/crypto/mlkem/../internal.h:1373:68:
error: unused parameter 'counter' [-Werror,-Wunused-parameter]
  OPENSSL_INLINE void boringssl_fips_inc_counter(
      enum fips_counter_t counter) {}
```

### Root cause

Apple Clang 21 (from macOS 26 Command Line Tools) is stricter than
older clang and flags unused parameters in inline functions. The
Bazel macOS default C++ toolchain applies `-Werror` via the
`treat_warnings_as_errors` feature, **but boringssl's own
`BUILD.bazel` also sets `copts = [..., "-Werror", ...]` at the
target level**.

**Flag ordering defeats the obvious fix.** Initial attempt:

```bash
build --copt=-Wno-error
build --features=-treat_warnings_as_errors
```

Neither worked. Reason: `--copt` is applied *before* target-level
`copts`; boringssl's target `-Werror` appears **later** on the
command line and wins. `--features=-treat_warnings_as_errors`
addresses toolchain features, not per-target copt strings, so
also moot.

### Detection

Reading `--verbose_failures` output showed the actual command line:

```
... -Wno-error -std=c++20 ... -Werror -Wunused-parameter ...
  ^^^^^^^^^^^^^                 ^^^^^^^
  our global            (boringssl's target-level copt, wins)
```

The flag-ordering asymmetry was visible only in the raw command.

### Resolution

Use `--per_file_copt` which Bazel applies **after** target-level
copts:

```
build --per_file_copt=external/.*@-Wno-error
build --per_file_copt=external/.*@-Wno-unused-parameter
build --host_per_file_copt=external/.*@-Wno-error
build --host_per_file_copt=external/.*@-Wno-unused-parameter
```

This demotes `-Werror` back to warning-only **only for files under
`external/`** — our own `engine_cpp/` targets keep strict defaults
and can individually re-opt-in via `copts=["-Werror"]`.

### Prevention

- `.bazelrc` carries an inline comment explaining flag ordering so
  future readers don't repeat the `--copt` attempt.
- Every third-party static lib brought in via http_archive or
  `rules_foreign_cc` gets a default `--per_file_copt` suppression
  pattern.

### Lessons

1. **Bazel's copt precedence is**
   `toolchain → --copt/--cxxopt → target.copts → --per_file_copt`.
   To win globally, use `--per_file_copt`.
2. **"Don't treat warnings as errors" is not one flag.** There are
   at least three places it can be set (toolchain feature, global
   copt, target copt). Knowing which one binds requires reading
   the full invocation, not the abstract API.
3. **New compiler releases expose old dormant `-Werror`s.** Every
   Apple Clang / GCC major bump can light up warnings in upstream
   libs that ship `-Werror`. Accept that external-code is not your
   quality gate; protect only your own.

---

## Incident 03 — whisper.cpp `_vDSP_*` undefined at engine link

**Date**: 2026-04-13  **Severity**: S3  **Duration**: ~10 min
**Related commit**: `d8b4988`

### Symptom

After `cmake` rule successfully produced `libwhisper.a` + `libggml*.a`
(7m38s cold build), linking those into the engine binary failed:

```
Undefined symbols for architecture arm64:
  "_vDSP_vadd", referenced from: _ggml_compute_forward_acc in libggml-cpu.a
  "_vDSP_vdiv", referenced from: _ggml_compute_forward_div in libggml-cpu.a
  "_vDSP_vmul", ... "_vDSP_vsadd", ... "_vDSP_vsmul", ... "_vDSP_vsub"
ld: symbol(s) not found for architecture arm64
```

### Root cause

ggml's CPU backend uses Apple's **Accelerate framework** for basic
SIMD math (`vDSP_*` is Accelerate's vector DSP API) on macOS. This
is an **unconditional** link dependency — it applies even when
`GGML_BLAS=OFF` (we had disabled BLAS for CPU-baseline Session 4a).
The static library has *references* to `vDSP_*` symbols but no
link directive telling downstream consumers to add
`-framework Accelerate`.

### Detection

The linker error named `vDSP_*` explicitly — searching "vDSP" on
Apple's docs pointed to the Accelerate framework within seconds.

### Resolution

Add a consumer-facing `cc_library` wrapper in the root module
(`engine_cpp/third_party/whisper_cpp:whisper_cpp`) that carries the
platform-specific `linkopts`:

```python
cc_library(
    name = "whisper_cpp",
    deps = ["@whisper_cpp//:whisper_cpp_cmake"],
    linkopts = select({
        "@platforms//os:macos": ["-framework", "Accelerate",
                                 "-framework", "Foundation"],
        "//conditions:default": [],
    }) + select({
        "//engine_cpp:backend_metal": ["-framework", "Metal",
                                       "-framework", "MetalKit"],
        "//conditions:default": [],
    }),
)
```

Two follow-on lessons surfaced during the fix:

- Nested `select()` is illegal; use `select() + select()` via list
  concat.
- `@platforms` is not visible from inside an `http_archive`'d BUILD
  file (different repo context); the wrapper had to live in our
  own repo, not in the external BUILD.

### Prevention

- All third-party `rules_foreign_cc` libraries will get a consumer
  wrapper in `engine_cpp/third_party/<dep>/BUILD.bazel` that carries
  linkopts, not only in the external BUILD. Documented in
  `CONTRIBUTING.md` (pending).
- Session 4a commit message captured the specific "vDSP_ means
  Accelerate" knowledge.

### Lessons

1. **Static C libraries under CMake often don't propagate their
   link dependencies.** The consumer must re-derive them from
   `CMakeLists.txt` / runtime docs. rules_foreign_cc's `cmake` rule
   does not magically export `INTERFACE_LINK_LIBRARIES`.
2. **Every Apple OS framework is an implicit transitive dep.**
   When a cross-platform lib "just works" on macOS, at least one
   of `Accelerate`, `CoreFoundation`, `Foundation`, `Metal`,
   `CoreAudio`, `AVFoundation` is likely involved silently.
3. **Read the undefined-symbol message carefully before
   googling.** The `_vDSP_` prefix was a direct signal; 10 seconds
   of Apple docs would have saved 10 minutes of guessing.

---

## Incident 04 — buf pre-commit v1.34.0 unaware of v2 lint categories

**Date**: 2026-04-12  **Severity**: S3  **Duration**: ~10 min
**Related commit**: `6b1a43e`

### Symptom

CI `Pre-commit hooks` job failed on every push:

```
buf lint.................................................Failed
- hook id: buf-lint
- exit code: 1
Failure: "STANDARD" is not a known id or category
```

Yet the `Proto lint` CI job (which uses `bufbuild/buf-setup-action@v1`)
passed. Two buf invocations, one green, one red, on the same
`buf.yaml`.

### Root cause

`.pre-commit-config.yaml` pinned the buf pre-commit hook at
`rev: v1.34.0`. `buf` pre-commit hook v1.34.0 installs a **buf CLI
v1.34.0** under the hood. Our `buf.yaml` declared `version: v2`
and used the `STANDARD` lint category (introduced in the v2 config
format). Buf CLI v1.34 parses `version: v2` but does not yet know
the `STANDARD` category — it was renamed from `DEFAULT` → `STANDARD`
as part of the v2 migration and v1.34 is too old to see the new
name.

Meanwhile `bufbuild/buf-setup-action@v1` installs the **latest** buf
CLI (v1.67.0 at the time), which handles v2 config fine.

Two lint runs, two tool versions, only one working.

### Detection

The error text `"STANDARD" is not a known id or category` was
specific enough to find the relevant buf GitHub issue in one
search. Contrast of passing vs failing CI jobs narrowed the
hypothesis to "version mismatch between the two buf invocations".

### Resolution

Bumped the pre-commit pin to match current buf:

```diff
  - repo: https://github.com/bufbuild/buf
-   rev: v1.34.0
+   rev: v1.67.0
```

### Prevention

- Dependabot version updates for `.pre-commit-config.yaml` added to
  Phase 1 Session 5 checklist (ROADMAP).
- `.pre-commit-config.yaml` now has a comment on each `rev:`
  explaining "bump together with `buf.yaml` version changes".

### Lessons

1. **Pre-commit and CI tool versions drift independently.** A
   pre-commit rev pin is a tool-version pin; a GitHub Actions
   `@v1` is a reusable-action pin that typically installs latest.
   Two "buf" jobs can be running different binaries entirely.
2. **Cryptic `"FOO is not a known id"` errors are almost always
   version-mismatch between config file schema and parser.** When
   encountered, bisect **versions first**, code logic second.

---

## Incident 05 — GitHub Rulesets silently no-op on private free repos

**Date**: 2026-04-12  **Severity**: S5 (platform surprise)
**Duration**: ~5 min realization + redirect
**Related commit**: `6e90268` (README update reflecting visibility)

### Symptom

After creating a ruleset on `main` via the GitHub UI — require PR,
required approvers, status checks, block force-push, required
signatures — a test `git push origin main` succeeded **without**
triggering any ruleset enforcement. Rulesets looked "Active" in
Settings but were not being evaluated.

### Detection

The ruleset settings page displayed a banner that was easy to miss
until the behavior didn't match:

> "Your rulesets won't be enforced on this private repository
> until you move to GitHub Team organization account."

Verification via `gh api`:

```
$ gh api repos/BinHsu/aegis-core/rulesets --jq '.[] | .enforcement'
"active"
$ gh api repos/BinHsu/aegis-core --jq '.visibility'
"private"
```

Rulesets reported `active`, repo was `private`, on a personal Free
account → effectively unenforced.

### Root cause

**GitHub tier gating.** Rulesets as a feature require either:
- Public repository (any account), OR
- GitHub Team / Enterprise org account (paid) for private repos.

A private repo on a personal Free account gets the UI but not the
enforcement. The gating is disclosed in one UI sentence and not
reflected in the API's `enforcement: "active"` response.

### Resolution

Made the repo public (the project was designed as OSS from day one,
architecture docs + proto contracts are the early deliverables,
nothing sensitive was in the git history — verified via
`git log --all --name-only | grep -iE '\.env|secret|credential|key'`).

Once public, every previously-configured ruleset, private
vulnerability reporting, secret scanning, and Dependabot feature
came online.

### Prevention

- `docs/github-setup.md §0` "Repository Visibility" now documents
  the tier requirement explicitly so future contributors do not
  spend time debugging an enforcement no-op.
- The verification snippet in `docs/github-setup.md §Full
  Verification` queries both `.visibility` and `.enforcement` so a
  misconfigured state shows up in a single one-liner.

### Lessons

1. **API `"active"` does not mean enforced.** For GitHub, SaaS
   tier + repo visibility both gate ruleset enforcement. Always
   verify by **trying to violate a rule** (test push without
   signed commit, etc.) rather than trusting the status field.
2. **Platform free tiers quietly degrade features.** When a UI
   says "Active" but behavior says otherwise, check the docs for
   tier-based feature gates. This applies to GitHub, GitLab,
   Bitbucket, and similar.
3. **"Public" is often a one-way decision worth getting right
   early.** For Aegis the timing was lucky — docs/proto only, no
   sensitive git history — but the principle generalizes: decide
   repo visibility at bootstrap, before anything sensitive could
   leak in history.

---

## Incident 06 — rules_go go_bin_runner needs BUILD_WORKSPACE_DIRECTORY

**Date**: 2026-04-13  **Severity**: S3  **Duration**: ~30 min
**Related commit**: A1 wrapper-fix commit (this session)

### Symptom

Running our `tools/scripts/go.sh` wrapper from inside `gateway_go/`
errored with:

```
$ cd gateway_go && ../tools/scripts/go.sh fmt ./...
2026/04/13 14:13:36 open gateway_go/go.mod: no such file or directory
```

Even basic queries failed:

```
$ ../tools/scripts/go.sh env GOMOD
2026/04/13 14:21:22 open gateway_go/go.mod: no such file or directory
```

The error path is suspicious — `gateway_go/go.mod` is the path
**from the repo root**, not from cwd, so the binary clearly was
not honouring the current working directory.

### Root cause

`rules_go`'s `@rules_go//go` target builds a small Go program
(`go_bin_runner`) that wraps the SDK's `go` binary and looks up
the workspace via the `BUILD_WORKSPACE_DIRECTORY` environment
variable. That env var is **set by `bazel run`** when invoking the
binary, but our wrapper invokes the produced binary directly from
`bazel-bin/...`, so the var was unset and `go_bin_runner` fell
back to a workspace path that did not match where we actually
ran from.

### Detection

Setting `BUILD_WORKSPACE_DIRECTORY` explicitly fixed it
immediately:

```
$ BUILD_WORKSPACE_DIRECTORY="$(pwd)/.." ../tools/scripts/go.sh env GOMOD
/Volumes/.../aegis-core/gateway_go/go.mod
```

### Resolution

`tools/scripts/go.sh` now exports
`BUILD_WORKSPACE_DIRECTORY="$REPO_ROOT"` before exec'ing the
binary. Caller cwd determines module resolution; the env var
satisfies `go_bin_runner`'s workspace lookup.

### Prevention

- Inline comment in `tools/scripts/go.sh` explaining why the env
  var is set, with the exact error string a future reader might
  search for.
- The wrapper already exports a few helpful niceties; the env
  var is the only one that's load-bearing for correctness.

### Lessons

1. **Bazel-rule-provided binaries often expect Bazel-set
   environment.** When you bypass `bazel run` you also bypass
   the env contract that the rule's binary depends on. Read the
   rule's source (or runfiles wrapper) to learn what it needs.
2. **"no such file or directory" on a path you can `ls` is rarely
   a permission or filesystem issue.** It's almost always a
   subprocess being given the wrong cwd or a base-path env var.
   Test by hard-coding the env var to confirm the hypothesis
   before pursuing other directions (workspace mode, GOWORK,
   etc., none of which were the problem here).
3. **Wrapper scripts that proxy external binaries should explicitly
   set every env var the binary depends on**, not rely on the
   user's environment leaking through.

---

## Incident 07 — Linux CI link error: ggml's libgomp dependency

**Date**: 2026-04-13  **Severity**: S3  **Duration**: ~20 min
**Related commit**: Phase 1 closeout follow-up (this session)

### Symptom

Adding a `bazel-unit-tests` job to GitHub Actions surfaced a
Linux-specific link failure that had been silently dormant on the
macOS development machine:

```
/usr/bin/gcc @bazel-out/.../engine-2.params
bazel-out/.../libggml-cpu.a(ggml-cpu.c.o):
  ggml_compute_forward_mul_mat:    undefined reference to 'GOMP_barrier'
  ggml_graph_compute_thread.isra.0: undefined reference to 'GOMP_barrier' (×3)
  ggml_graph_compute._omp_fn.0:    undefined reference to 'GOMP_single_start'
                                   undefined reference to 'omp_get_thread_num'
                                   undefined reference to 'omp_get_num_threads'
  ggml_graph_compute:              undefined reference to 'GOMP_parallel'
collect2: error: ld returned 1 exit status
```

### Root cause

ggml's CMake enables `GGML_OPENMP=ON` by default. On Linux with GCC,
this introduces a hard runtime dependency on `libgomp` (the GNU
implementation of OpenMP); the symbols above (`GOMP_*`,
`omp_get_*`) live in `libgomp.so` and must be linked via `-lgomp`.

Our consumer `cc_library` wrapper at
`engine_cpp/third_party/whisper_cpp:whisper_cpp` carried Apple
linkopts (`-framework Accelerate` etc.) but no Linux equivalent —
the macOS build worked because Apple Clang ships an OpenMP runtime
shim by default. The Linux gap was invisible until CI ran for the
first time on `ubuntu-latest`.

### Detection

The new `bazel-unit-tests` CI job's failure log named the missing
symbols verbatim. `GOMP_*` is unambiguous — Linux GCC OpenMP runtime
— so the chain "ggml uses OpenMP → Linux needs libgomp → our
linkopts don't include it" was 30 seconds of search.

### Resolution

Two clean options:

| Option | Pro | Con |
|---|---|---|
| (A) Disable `GGML_OPENMP=OFF` in cmake cache_entries | One-line, no platform-specific linkopts | Threading falls back to pthreads — small perf loss |
| (B) Keep OpenMP ON, add `-lgomp` in linkopts on Linux | Faster threading on Linux | Two more `select()` cases on linkopts; matrix surface grows |

Picked (A) for Phase 1 — CPU-only baseline, no live performance
SLO yet. A Phase 2+ load test will tell us whether the perf delta
matters; if so, flip to (B) and add Linux linkopts. Inline comment
in `whisper_cpp.BUILD` documents the trade-off and the upgrade
path.

### Prevention

- The pattern "third-party static library has Linux-only runtime
  deps that surface only at link time" is the same shape as
  Incident #03 (`_vDSP_*` on macOS). The wrapper `cc_library` we
  built for #03 is the right place to enumerate Linux deps too —
  inline comment in `whisper_cpp.BUILD` flags the symmetry.
- The `bazel-unit-tests` CI job — added in the same Phase 1
  closeout — is the new safety net that caught this. Without it,
  the Linux gap would have remained dormant until the first
  contributor on a Linux dev box.

### Lessons

1. **macOS-only development hides Linux-only platform deps.** The
   first CI run on Linux is a high-value drift-check moment;
   schedule it sooner rather than later.
2. **Third-party libs with optional accelerators (OpenMP, BLAS,
   Metal, CUDA) often default-on per platform.** Audit the
   defaults explicitly and turn off everything you do not link
   for, even the "free" accelerators that look harmless.
3. **A new CI job often catches a class of issue, not a single
   issue.** When the `bazel-unit-tests` job lit up red, the lesson
   was "we have no Linux validation pre-merge," not "ggml has a
   bug." The fix is one line; the value is the new check itself.

---

## Incident 08 — `bazel run //:app_local --with-frontend` shutdown glacially slow (29 s)

| Field | Value |
| --- | --- |
| Date | 2026-04-14 |
| Severity | S3 — annoying dev-experience regression, not a ship blocker |
| Commit | `38f9150` |
| Phase | Phase 3 A-2 (N-child supervisor in `cmd/app_local`) |

### Symptom

After extending the Local-mode launcher with a third subprocess
(Vite dev server, opt-in via `--with-frontend`), Ctrl-C / SIGTERM
took **29 seconds** to return the shell. Engine + gateway + frontend
all received the signal, all three visibly exited, but the launcher
itself hung for the full duration, logging each child's grace-period
timeout and abandon-reap warning in sequence.

The two-child variant (engine + gateway only, pre-Phase-3) shut
down in ~3 ms. So the slowness appeared the moment we added a third
child.

### Root cause

Double-consumption of each child's single-consumer `wait` channel.

The supervisor had a fan-in goroutine per child:

```go
for _, c := range children {
    go func() {
        err := <-c.wait                        // consumes the channel
        exits <- childExit{name: c.name, err: err}
    }()
}
```

Then `select { case <-ctx.Done(): ... }` fired the teardown path,
which called `terminate(child)` for each. `terminate` in turn did:

```go
select {
case <-c.wait:                                 // blocks forever
case <-time.After(c.gracePeriod):
    ...
}
```

But the fan-in goroutine had **already drained `c.wait`** — it's a
buffered channel with exactly one payload — so `<-c.wait` inside
terminate blocked for the full grace period every single time, even
though the underlying process had exited 50 ms earlier. The log
showed `[gateway] bye` at T+4 ms and the grace-period warning at
T+10 s, with nothing in between.

Root cause is structural: we reached for "react to any unexpected
exit" (fan-in) without considering that the same event needs to be
observable by the ctx-done-driven teardown too. A one-shot `<-chan`
can serve at most one reader.

### Detection

Noticed the 29 s total only after visually timing a Ctrl-C with a
stopwatch (`kill -TERM; while kill -0; do :; done`). Before that,
the symptom was being misread as "Vite is slow to exit under
SIGTERM" — a plausible-but-wrong hypothesis because `[frontend]
Terminated: 15` was the last line before the gap.

### Red herrings (~45 min lost)

1. **"pnpm wrapper won't forward SIGTERM"** — true but tangential;
   the actual frontend processes DID eventually die. The grace-
   period was only SIGTERM → SIGKILL fallback semantics, not the
   bug.
2. **"set Setpgid on all children so grandchildren propagate"** —
   over-correction. Made the engine/gateway shutdown *worse*: with
   Setpgid=true on a plain Go binary that forks nothing, `cmd.Wait()`
   empirically hangs until the grace escalation, turning a 3 ms
   shutdown into a 10 s one. Reverted this to pgroup-only-for-
   frontend.
3. **"`cmd.Wait` is stuck on stdout/stderr pipe EOF because a
   detached grandchild holds the fd"** — actually true in the
   frontend case (esbuild daemons on macOS), but it wasn't the
   cause of the 29 s because engine and gateway had no
   grandchildren and still hung. This is real but downstream of
   the actual bug; we kept the `Process.Release()` fallback for it.

### Resolution

Commit `38f9150`:

1. Change `child.wait <-chan error` to `child.done <-chan struct{}`
   plus `child.waitErr *error`. The `done` channel is **closed**
   (never value-sent-to) by the waiter goroutine when cmd.Wait
   returns. A closed channel broadcasts to arbitrarily many readers,
   so both the fan-in and terminate observe the same exit without
   draining.
2. Apply `SysProcAttr.Setpgid=true` only to the frontend child
   (where pnpm→node→vite grandchildren require pgroup-level
   signalling). Engine and gateway stay direct-pid.
3. Keep the `Process.Release()` post-SIGKILL fallback for the
   detached-grandchild pipe-hold case, but it's now rarely hit.

Measured shutdown time after fix: **0.03 s** (1000× faster).

### Prevention

- **Go-specific**: whenever a channel is a "completion event that
  multiple observers need," use close-broadcast (`close(ch)` +
  `<-ch`) not one-shot send. The type signature `<-chan struct{}`
  makes the intent visible at call sites — zero-size payload is a
  standard idiom for "this is a signal, not a value".
- **Supervisor pattern**: if your fan-in reader is the *only* thing
  that reads a per-child event, any other code that needs to
  observe the event must route through the fan-in's published
  state — never through the source channel directly.
- **Darwin quirk noted**: `SysProcAttr.Setpgid=true` on a Go binary
  that forks nothing triggers a slow-reap path in `cmd.Wait` on
  macOS. Applying pgroup-scoped supervision should be selective;
  default to off, opt in per child when grandchild signal
  propagation is genuinely required.

### Lessons

1. **"The process exited 50 ms ago" and "my launcher doesn't know
   yet" are different facts.** Confusing them costs real time.
   Timing the shutdown with a stopwatch, not inspecting logs,
   turned out to be the cheapest way to separate process-exit
   latency from supervisor-observability latency.
2. **Two bugs can look like one bug.** The Vite slow-SIGTERM thing
   and the fan-in channel drain were both present, and fixing the
   first one alone left the second invisible because its effect
   was being masked by the first's grace period. Each layer of
   fix stripped one symptom; the final fix got us from 29 s to
   30 ms.
3. **N-child refactor wasn't gratuitous.** It surfaced a
   supervisor-pattern bug that was latent in the two-child variant
   — the old code happened not to exercise the ctx-done terminate
   path because engine/gateway always exited cleanly within the
   fan-in's consumption window. Adding a third child that behaves
   differently exposed the structural issue. Add N-ness *before*
   you think you need it; it catches this class of latent bug.
4. **Setpgid is not free.** The reflex "put children in their own
   process groups so I can signal them as a unit" is usually the
   right move on Linux. On darwin it interacts with `cmd.Wait`'s
   fast-reap in ways I can't yet explain in detail — benchmark
   before applying globally.

---

## Process notes

- Incidents here cover **development-time** blockers, not a
  running production system. Once the system is in Phase 4+ with
  real users, this file will split: operational incidents go to a
  separate `ops/incidents.md` on the `aegis-aws-landing-zone` repo (customer
  impact is out of scope for the application repo).
- Each postmortem links back to the commit(s) that resolved it;
  the commit messages themselves carry the nitty-gritty details
  (exact error text, full diff). This file is the narrative
  layer.
- Severity is a **development severity** scale as defined in the
  header, not the SRE SEV1/2/3 convention used at runtime.

## Review cadence

- Update on every S2/S3 incident encountered.
- Quarterly: re-read top 5 "Lessons" across incidents and check
  whether they remain true; archive lessons that have been
  absorbed into ADRs or tooling.
