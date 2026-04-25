# Incident Postmortems — Aegis Core

<!-- session-close-review: incidents since prior session — add a postmortem if Rule 7 criteria fired (≥15min blocker, ≥2 failed fix attempts, root cause >1 layer below surface) -->

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

## Incident 09 — pion/opus rejects every browser Opus frame; refactor decode to engine

**Date**: 2026-04-14 — 2026-04-15  **Severity**: S3  **Duration**: ~1 day end-to-end (discovery → ADR → wire flip)
**Related commits**: `1984f19`, `748fde2`, `8292470`, `<this-commit>`
**Related ADR**: [ADR-0016](adr/0016-opus-decode-on-engine.md)

### Symptom

During the Phase 3 LAN-phone smoke test (iOS Safari joining as the
host, gateway running on a laptop on the same Wi-Fi, engine on
loopback), the gateway log spammed once per 20 ms audio frame:

```
pipeline: opus decode: unsupported configuration mode: 3
```

Every Opus frame from the phone failed to decode; the engine
received zero PcmChunks; the transcript stayed empty for the entire
test session. Same result on Chrome on Android.

### Root cause

`github.com/pion/opus` is a pure-Go Opus implementation that has
**not** reached coverage parity with `libopus`. RFC 6716 defines
three Opus coding modes — **SILK** (low-rate speech), **CELT**
(music), and **Hybrid** (both, stacked). WebRTC browsers
**routinely negotiate Hybrid** at common bitrates (config field 14–15,
which surfaces as the "mode 3" error in pion/opus). pion/opus
refuses to decode these frames with the error verbatim above.

The gateway's loopback unit tests had been silent on this because
they used pre-recorded fixtures encoded with constrained config
that pion/opus *does* support. Real browsers exposed the gap
immediately on the first non-loopback test.

### Detection

The error message in the gateway log was the dead giveaway:
specifically, the words "configuration mode: 3" pointed straight at
the Opus codec's mode field rather than at any networking or
session-state symptom. The instinct was initially to look at WebRTC
negotiation (was the SDP wrong? wrong codec selected?), which cost
~10 minutes; the codec-mode reading of the message redirected
attention to the right layer.

### Resolution

A domain-boundary refactor rather than a code-level patch (the more
intuitive "patch pion/opus or fork it" path was rejected after
~30 min of analysis):

1. **Decision** ([ADR-0016](adr/0016-opus-decode-on-engine.md)):
   move Opus decode from the Go gateway to the C++ engine. Codec
   work belongs in the audio-processing domain (alongside whisper.cpp
   and any future DSP), not in the session-transport BFF. Gateway
   forwards RTP payloads verbatim.
2. **Day 1 (`1984f19`)** — proto + build infra: added `OpusChunk`
   variant to `IngestMessage.payload` (proto3 back-compat); added
   libopus 1.5.2 via `rules_foreign_cc` cmake; landed
   `aegis::audio::OpusDecoder` C++ wrapper class with
   encode→decode roundtrip unit test.
3. **Day 2a (`748fde2`)** — engine side: Session state machine
   gained a kOpus branch; lazy-init OpusDecoder per session; decode
   errors are log-and-drop (single corrupt 20 ms frame must not
   tear down a session).
4. **Day 2b (`8292470`)** — gateway side: removed pion/opus,
   `WriteRTPPayload` now emits OpusChunk; dropped the dep from
   `go.mod` + `MODULE.bazel`.
5. **Day 2c (this commit)** — docs (`ARCHITECTURE.md`, `ROADMAP.md`,
   this entry) + libopus build pinned to
   `CMAKE_OSX_DEPLOYMENT_TARGET=11.0` to silence the
   `object file was built for newer 'macOS' version (26.3) than
   being linked (11.0)` warnings on the engine binary link.

### Prevention

- **Test on real browsers, early.** pion/opus's loopback fixtures
  passed with flying colors; a phone in the room did not. Adding a
  "real-browser smoke test" to the Phase 3 acceptance gate would
  have caught this two days earlier. ROADMAP item logged.
- **Be skeptical of pure-language codec libraries.** The pull of
  "no cgo, single language" is real, but codec coverage is the
  long tail of standards-compliance work that volunteer Go ports
  rarely complete. For codec-heavy paths, the FFI cost of using
  the canonical C library is usually the right cost to pay. (The
  alternative of cgo-wrapping libopus in Go was also considered
  and rejected — see ADR-0016 §"Why not A'".)
- **The macOS-deployment-target warning is a class.** libopus is
  the second time the SDK-vs-link mismatch has surfaced (whisper.cpp
  has the same warning on libggml-cpu). When adding any
  `rules_foreign_cc` cmake() target, the cache_entries should
  default to including `CMAKE_OSX_DEPLOYMENT_TARGET` aligned with
  Bazel's apple toolchain. Open a separate issue to fix the
  whisper.cpp side.

### Lessons

1. **Look at the literal error string before climbing the stack.**
   "configuration mode: 3" is a codec-domain phrase. Treating it
   like a generic decode failure ("maybe the bytes are wrong, maybe
   the network corrupted it") wastes time. Read the words.
2. **Refactor at the domain boundary, not at the code seam.** Two
   "smaller" fixes (patch pion/opus, or wrap libopus in cgo on the
   gateway) both *looked* cheaper than a multi-component refactor.
   Both would have moved the fault back into the gateway, which is
   the wrong domain for codec work. The "biggest" diff was the
   right one because it made the boundary correct, and the
   subsequent commits were small and uncontroversial as a result.
3. **A planned 2-day refactor that *actually takes* 2 days is a
   gift.** The day-1/day-2 split was specified in ADR-0016 up
   front; both days hit their scope, with one descope (the in-test
   encode-decode roundtrip replaced the "checked-in fixture"
   variant) explained in the test file's header. Plans that survive
   contact with reality without slipping are worth noting precisely
   *because* they're rare.
4. **The ADR was load-bearing.** Going straight to code on this
   one would have left the "why not A'" question unanswered for
   future readers. Because A and A' both *look* simpler than C
   from a code-diff perspective, anyone re-evaluating the choice
   needs the rationale on file or they'll relitigate it.

---

## Incident 10 — ggml "0.9.8" ≠ "0.9.8": llama.cpp b8595's cherry-picked symbols

**Date**: 2026-04-17  **Severity**: S3  **Duration**: ~40 min end-to-end (discovery → root cause → triple bump → CI guard)
**Related commit**: Phase 3b Slice 4 (this session)

### Symptom

Landing the first GGMLEmbedder integration test against real bge-m3 Q4_K_M
weights — `//engine_cpp/tests/integration:bge_m3_embed_test` — died at the
link step:

```
Undefined symbols for architecture arm64:
  "_gguf_init_from_file_ptr", referenced from:
      llama_model_loader::llama_model_loader(...) in libllama.a[21](llama-model-loader.cpp.o)
  "_gguf_write_to_file_ptr", referenced from:
      llama_model_saver::save(...) in libllama.a[22](llama-model-saver.cpp.o)
ld: symbol(s) not found for architecture arm64
```

The `GGMLEmbedder` library target alone (`:ggml_embedder`) built fine —
static libraries defer undefined-symbol errors to the final link. The
error surfaced only when linking the test binary that combines
`libllama.a`, `libwhisper.a`, and the shared `libggml*.a`.

### Root cause

Same version number, divergent source.

`MODULE.bazel` pinned the three-way triple with a confident banner:

```
║ Current pin: ggml v0.9.8
║   whisper.cpp v1.8.4 — bundles ggml 0.9.8
║   llama.cpp b8595    — bundles ggml 0.9.8
```

All three tarballs' `ggml/CMakeLists.txt` did declare
`GGML_VERSION 0.9.8`. The claim was self-consistent by the version
string. The actual source was not.

Checking each tarball's `ggml/include/gguf.h`:

- Standalone `ggml-0.9.8`: declares `gguf_init_from_file` (path-based).
- Whisper `whisper.cpp-1.8.4`/ggml: same — declares only
  `gguf_init_from_file`.
- Llama `llama.cpp-b8595`/ggml: declares **both**
  `gguf_init_from_file` AND `gguf_init_from_file_ptr` (FILE-pointer
  variant), plus `gguf_write_to_file_ptr`.

The `_ptr` variants were added to ggml upstream between v0.9.8 and
v0.9.9. `ggml-org/llama.cpp`'s `b8595` tag was cut after a cherry-pick
of those variants into its vendored ggml tree — **but the cherry-pick
did not bump `GGML_VERSION_PATCH`**. The version string stayed at
0.9.8; the symbol table quietly grew.

Building `libllama.a` against the llama-bundled ggml headers produces
object files that reference `gguf_*_ptr`. Linking that `libllama.a`
against the standalone `@ggml` v0.9.8 (which does NOT export the `_ptr`
symbols) is therefore guaranteed to fail — except we never tried the
link in CI until today. `bazel build //engine_cpp/cmd/engine:engine` in
the existing CI only linked whisper + ggml; it did not pull in llama,
so the drift was dormant.

### Detection

Three signals lined up:

1. The linker named the exact missing symbols — `_gguf_init_from_file_ptr`,
   `_gguf_write_to_file_ptr`.
2. `grep -n 'gguf_init_from_file' <each tarball>/ggml/include/gguf.h`
   immediately showed the asymmetry: llama's bundled header had the
   `_ptr` declaration; the other two did not.
3. ggml upstream tag list confirmed v0.9.9 exists and introduces the
   `_ptr` functions.

The red herring worth flagging: the first instinct was "all three are
at 0.9.8, so this is a build-configuration issue, not a version
issue." That instinct is what ADR-0021 P3's grep-based check was
originally designed to validate. Had we implemented P3 that way before
hitting this, the check would have been *green* and the link would
still have failed — the check was measuring the wrong thing.

### Resolution

Single minimal change: bump standalone `@ggml` from v0.9.8 → v0.9.9.
v0.9.9 is the first upstream tag that exports the `_ptr` variants, so
it covers llama.cpp b8595's references. ggml API evolution in the
0.9.x line is additive; whisper.cpp v1.8.4's bundled ggml 0.9.8 uses
a strict subset of v0.9.9's symbols, so the bump is backward-compatible
for whisper. Verified by re-running
`//engine_cpp/tests/integration:whisper_transcribe_test` after the bump.

`MODULE.bazel`'s banner comment was rewritten to explain the asymmetric
pin: standalone ggml is intentionally ahead of the consumers' vendored
version numbers, because llama.cpp ships its own cherry-pick that
effectively requires a post-0.9.8 ggml.

### Prevention

Both layers of ADR-0021 P3 landed in the same session:

1. **Grep-based drift script** (`tools/scripts/check_ggml_versions.sh`):
   fetches each archive and compares `GGML_VERSION_*`. Fails ONLY when
   standalone `@ggml` is **older** than a consumer's bundled ggml —
   the wrong-direction drift that breaks the link. Does not hard-fail
   on the compatible "standalone ahead of consumers" state that this
   incident resolved into.
2. **Integration-test link step in CI** (`.github/workflows/ci-baseline.yml`):
   `bazel build //engine_cpp/tests/integration/...`. This is the
   authoritative gate — the link step either succeeds or fails
   regardless of what the version string claims. Catches "same number,
   divergent source" drift that Layer 1 provably cannot.

`CONTRIBUTING.md §Upgrading the ggml triple` documents the procedure
and explicitly calls out this incident so future maintainers know
version-string parity is *not* sufficient.

### Lessons

1. **Upstream version numbers are suggestions, not contracts.** Cherry-
   picks into vendored subtrees routinely preserve the old version
   string. When a library embeds a dependency, the embedded dep's
   declared version tells you what the embedder *started from*, not
   what the embedded source currently contains.
2. **"Check the version numbers" is a drift signal, not a drift proof.**
   A useful CI check for version-coupled deps MUST include a step that
   exercises the actual integration (here: linking whisper + llama
   against the shared ggml). If the check is cheaper than the real
   thing, it measures something cheaper than correctness.
3. **The ADR was right about the pattern, wrong about the
   implementation.** ADR-0021 correctly identified that the three deps
   form a coupled triple needing a P3 check. The ADR's sketched
   implementation ("grep for GGML_VERSION_MAJOR/MINOR/PATCH and assert
   equality") would have been green through this incident. The
   incident forced the P3 implementation to grow a second, authoritative
   layer. ADRs that predict patterns are load-bearing; ADRs that
   prescribe mechanisms should be re-read at implementation time, not
   copied verbatim.
4. **Static-linking errors show up at the combined binary, not the
   component library.** Building `//engine_cpp/src/inference:ggml_embedder`
   was green the whole time. Building `//engine_cpp/cmd/engine:engine`
   was green because it did not yet depend on the embedder. The first
   place the link actually exercised both consumers of shared ggml was
   an integration test target — which is exactly why the new CI step
   targets integration-test builds, not library builds.

---

## Incident 11 — grpc `cc_grpc_library` + `strip_import_prefix` = virtual_imports protoc path mismatch

**Date**: 2026-04-17  **Severity**: S3  **Duration**: ~40 min end-to-end (three failed structural attempts → pivot to checked-in protos)
**Related commit**: Phase 3b Slice 5 (PR #15 — should have been added at the time per Rule 7; retroactively recorded per session-close marker)

### Symptom

Attempting to vendor Qdrant's proto tree via `http_archive` + a
same-package `cc_grpc_library` produced two different protoc
failures in succession, each looking like a different problem:

**Attempt 1 — consumer-package `cc_grpc_library`**:

```
ERROR: in _generate_cc rule //engine_cpp/third_party/qdrant:_qdrant_cc_grpc_grpc_codegen:
  fail: 'bazel-out/darwin_arm64-fastbuild/bin/external/_main~_repo_rules~qdrant/_virtual_imports/qdrant_proto/collections.proto'
  does not lie within 'engine_cpp/third_party/qdrant'.
```

**Attempt 2 — move `cc_grpc_library` into the external archive's BUILD**:

```
protoc failed: Could not make proto path relative:
  external/_main~_repo_rules~qdrant/_virtual_imports/qdrant_proto/collections.proto:
  No such file or directory
```

### Root cause

Two overlapping quirks that look like one bug:

1. grpc's `cc_grpc_library` rule requires its `srcs` proto_library
   to sit in the SAME Bazel package as the rule itself. Splitting
   the proto_library (in the external archive's BUILD) from the
   cc_grpc_library (in a consumer wrapper BUILD) fails at the
   analysis phase with "does not lie within" — hence attempt 1.
2. When proto_library uses `strip_import_prefix` (to preserve
   Qdrant's `import "qdrant_common.proto";` bare-filename internal
   imports), Bazel creates a `_virtual_imports/<library>/` tree
   and routes protoc invocations through it. grpc's
   `grpc_cpp_plugin` reads the source path DIRECTLY (not through
   the virtual mapping), so the protoc invocation fails with
   "Could not make proto path relative" — attempt 2.

Each attempt surfaces a different-looking error, and the two
errors do not obviously share a root until you realize both
trace back to the combination of `strip_import_prefix` +
`cc_grpc_library` on an external-repo proto_library. A
single-file proto_library (like our `proto/aegis/v1/aegis.proto`)
never triggers either quirk because it has no inter-proto
imports needing strip_import_prefix.

### Detection

The second failure's "No such file or directory" under
`_virtual_imports/` was the load-bearing signal — virtual imports
are a Bazel synthesis, not a filesystem fact, so protoc expecting
to stat the path means it's receiving the Bazel-internal layout
rather than a resolved filesystem path. Once that was visible,
grepping the grpc/bazel/generate_cc.bzl source (available in the
external repo cache) confirmed the plugin does not consult
`-I` paths the way bazel's native proto_library does.

### Resolution

**Pivot away from http_archive vendoring entirely**. Check the six
Qdrant proto files directly into `proto/qdrant/v1.17.1/` with an
in-tree BUILD.bazel. The checked-in layout does not need
`strip_import_prefix` because the protos sit at a Bazel package
root where bare-filename imports resolve naturally. Upgrade
procedure + provenance (upstream commit, tarball SHA-256, license)
captured in `proto/qdrant/v1.17.1/PROVENANCE.md` — `MODULE.bazel`
carries a brief comment explaining why http_archive was rejected.

Secondary refinement once the protos were checked in: a single
combined proto_library with six srcs passed cc_proto_library but
STILL failed cc_grpc_library's strict public-import check
("`X` seems to be defined in `collections.proto`, which is not
imported by `collections_service.proto`"). Fix: split into one
proto_library per file with explicit inter-proto `deps`, then one
cc_grpc_library per service proto, then a thin `cc_library` umbrella
(`qdrant_cc_grpc`) aggregating both service grpc libs for downstream
convenience.

### Prevention

- The in-tree vendor pattern (`proto/<vendor>/<version>/`) is now
  the default for third-party protos with inter-proto imports.
  `PROVENANCE.md` + `MODULE.bazel` comment block together point
  any future contributor at this incident.
- `buf.yaml` gains `excludes: [proto/qdrant]` so the vendored tree
  is not lint-gated by our STANDARD config — upstream protos
  follow upstream's conventions, not ours.
- Because the grpc plugin's virtual_imports handling is an upstream
  Bazel/grpc interaction we cannot easily fix, no new CI check is
  warranted; the in-tree pattern avoids the class of bug entirely.
- ADR-0021 P3's "integration-test link check" in CI still catches
  different-but-related drift (shared ggml version mismatches); no
  additional gate needed for the proto-vendor flavor.

### Lessons

1. **Bazel's proto virtualization is load-bearing but leaky.**
   `strip_import_prefix` works cleanly with native
   `cc_proto_library` because protobuf's protoc invocation follows
   Bazel's `-I` plumbing. It breaks with `cc_grpc_library` because
   grpc's codegen plugin has its own path resolution. Mixing the
   two on an external-repo proto_library is a supported-sounding
   configuration that isn't actually robust. Diagnostics are
   misleading because each wrong setup fails at a different layer.
2. **The cost/benefit of "vendor via `http_archive`" collapses for
   tiny proto-only trees.** Qdrant's six .proto files are ~few
   thousand lines total, text, and bumping them is a readable
   git diff instead of an opaque SHA change. Reserve `http_archive`
   for trees where build-time access to source is load-bearing
   (whisper.cpp, ggml, llama.cpp — all of which have
   `rules_foreign_cc` cmake steps that need the source).
3. **Rule 7's session-close discipline is about honesty across
   sessions, not just within one.** This postmortem should have
   been written in the PR #15 session per Rule 7's ≥15min + ≥2
   failed-attempts criteria. It was skipped because the session's
   energy was on "ship Slice 5" rather than "record what just
   hurt." Adding the `session-close-review` marker to
   `docs/incidents.md` (this session) is the mechanism to prevent
   that omission from recurring — the marker's grep will fire at
   every future session close, not just the one where the
   incident happened.

---

## Incident 12 — `pkg_tar` `remap_paths` silently no-ops; CI smoke fails twice on PR #28

**Date**: 2026-04-19  **Severity**: S4  **Duration**: ~25 min end-to-end (two failed CI runs → local tar inspection → fix)
**Related commits**: `aa111c4` (initial Slice 1), `c142fb0` (false-fix CI tag), `356201d` (real fix: pkg_files+renames)

### Symptom

Phase 4a Slice 1 introduced an OCI image build for the Go gateway,
plus a CI smoke step that loads the image into Docker and curls
`/healthz`. The smoke step failed twice with two different-looking
errors before resolving.

**Failure A** (`aa111c4`):
```
Loaded image: aegis-gateway:dev-local
+ docker run … aegis-gateway:ci-smoke
Unable to find image 'aegis-gateway:ci-smoke' locally
docker: Error response from daemon: pull access denied for aegis-gateway,
  repository does not exist or may require 'docker login'
```

**Failure B** (`c142fb0`, after first "fix"):
```
+ docker run … aegis-gateway:dev-local
docker: Error response from daemon: failed to create task for container:
  failed to create shim task: OCI runtime create failed: runc create
  failed: unable to start container process: error during container
  init: exec: "/usr/local/bin/gateway": stat /usr/local/bin/gateway:
  no such file or directory
```

### Root cause

Two separate bugs stacked, with the second one masked by the first.

1. **`oci_load.repo_tags` is not overridable via `bazel run -- <tag>`.**
   The first CI step did `bazel run //packaging/gateway:image_load --
   aegis-gateway:ci-smoke`, expecting the trailing argument to set
   the loaded tag. `oci_load` reads `repo_tags` from its BUILD
   attribute (hard-coded to `aegis-gateway:dev-local` in our case)
   and ignores positional argv. Image got loaded under `dev-local`;
   `docker run aegis-gateway:ci-smoke` then failed because no such
   tag existed locally.

2. **`pkg_tar`'s `remap_paths` shortcut silently fails on
   leading-slash mismatch.** The actual binary inside the layer tar
   was `usr/local/bin/gateway_linux_amd64` (the cross-binary's rule
   name), not the entrypoint-friendly `gateway`. The original
   BUILD.bazel declared:
   ```bzl
   pkg_tar(
       …,
       remap_paths = {
           "/usr/local/bin/gateway_linux_amd64": "/usr/local/bin/gateway",
       },
   )
   ```
   Tar entries are conventionally relative (no leading slash), so
   the rename key never matched any path in the tar — silently. No
   warning, no error. The image was built with the wrong entrypoint
   path, and `runc` reported the container init failure only at
   runtime.

### Detection

Failure A's "pull access denied" was a misleading red herring — the
real issue was a tag mismatch, not a registry auth problem. Reading
the line two above (`Loaded image: aegis-gateway:dev-local`)
made the mismatch visible.

Failure B was caught by extracting the layer tar locally:
```bash
tar -tvf bazel-bin/packaging/gateway/gateway_layer.tar
# usr/local/bin/gateway_linux_amd64   <— wrong filename
```
That immediately confirmed the rename did not happen.

A red herring during attempt to fix Failure B: my first `remap_paths`
correction (removing the leading slash) ran `bazel build` and got
"up-to-date" — the action cache decided the change wasn't material
and didn't rebuild. The tar still showed the old filename, but for
~5 minutes I assumed the fix had taken effect and was confused why
the path was unchanged. Cache invalidation gotcha.

### Resolution

Two-step fix across two commits:

- `c142fb0`: drop the misleading `bazel run -- <tag>` argv override
  in CI; reference the BUILD-defined `aegis-gateway:dev-local` tag
  directly. Comment in the workflow file records why argv override
  doesn't work.
- `356201d`: replace `pkg_tar(..., remap_paths = ...)` with the
  documented `pkg_files(..., renames = ...) → pkg_tar(srcs = …)`
  pattern. `renames` works on label keys, not tar paths, and is
  the rules_pkg-recommended mechanism for renaming files inside a
  package. Local verification: `tar -tvf` now shows
  `usr/local/bin/gateway` as expected.

### Prevention

- **Use `pkg_files` + `renames` over `pkg_tar.remap_paths` for any
  rename that matters.** `remap_paths` is the convenient shortcut
  that fails silently; `pkg_files` is the verbose but correct
  pattern. The `packaging/gateway/BUILD.bazel` comment block
  records this tradeoff so future contributors don't re-discover it.
- **Always `tar -tvf` the output of a packaging rule before
  building the image on top.** Image-build-then-debug-at-runtime
  is a slow loop; checking the tar directly is sub-second. This
  belongs in any future packaging slice's pre-PR checklist.
- **CI smoke must `set -euxo pipefail`** (already in place in this
  step) AND echo the tag actually used — the `set -x` trace was the
  load-bearing diagnostic for Failure A; without it the mismatch
  would have been invisible in the GitHub Actions log.
- No new ADR or CI gate warranted. The pattern is documented in
  `packaging/gateway/BUILD.bazel`; future packaging slices (engine
  Slice 4, frontend Slice 5) inherit the convention by example.

### Lessons

1. **Convenience APIs that fail silently are worse than verbose
   APIs that fail loud.** rules_pkg's `remap_paths` is one line
   shorter than `pkg_files + renames`, but the silent no-op cost
   ~25 minutes of CI cycles + log-reading. The verbose pattern's
   extra rule declaration is a one-time cost; the silent-failure
   class of bug is a recurring tax. Prefer verbose.
2. **"Fixed locally, build says up-to-date" is a smell.** Bazel's
   action cache is fast and aggressive; an attribute change that
   "should" trigger a rebuild but doesn't usually means the change
   wasn't structurally meaningful (wrong attribute name, wrong
   key format, no-op edit). Re-extract the actual output file
   instead of trusting that the build re-ran.
3. **Two stacked bugs with different-looking errors are
   indistinguishable from one bug until you fix the first.**
   Failure A and Failure B were genuinely different root causes,
   but the workflow ("smoke failed → look at last error → guess →
   push fix") only surfaces them sequentially. Mitigation: when
   the surface error changes after a fix, treat it as a NEW
   incident, not a continuation. Otherwise it's tempting to assume
   the fix "almost worked" and patch around the new symptom rather
   than read it cleanly.

---

## Incident 13 — Bazel cache thrashes every invocation from a different shell (PATH hash instability)

**Date**: 2026-04-20  **Severity**: S3  **Duration**: ~2 hours end-to-end (symptom recognition → misattribution → `--explain` diagnosis → one-line fix → verification)

**Related commit**: *this commit*

### Symptom

Every invocation of `./tools/bazelisk/bazelisk run //:app_local` from the developer's interactive terminal took ~10 minutes, even when the previous invocation had finished seconds earlier and **no source file had changed**. The bottleneck consistently surfaced as:

```
[8,666 / 8,673] Foreign Cc - CMake: Building llama_cpp_cmake; 368s darwin-sandbox
```

i.e. the `rules_foreign_cc` CMake wrapper was rebuilding `llama.cpp` (and by symmetry `whisper.cpp`) from scratch on every invocation. The engine binary was re-compiling `engine_cpp/cmd/engine/main.cc` and `engine_cpp/src/inference/ggml_embedder.cc` even when the only edit touched **Go** files in the gateway.

Knock-on effect: every end-to-end LAN debug iteration cost 10 minutes before the binary was even runnable, making WebRTC / transcript debugging intractable.

### Root cause

Bazel's action-cache key includes the client environment exposed to actions. By default, the `PATH` environment variable is forwarded as-is from the calling shell. Our two invocation paths have **different PATHs**:

- The developer's interactive `zsh` has `/opt/homebrew/bin`, `/Users/bin.hsu/.pyenv/shims`, the default system path.
- Tooling subprocesses (editor helpers, Claude Code, CI runners locally) have an **additional** entry like `/Users/bin.hsu/.claude/plugins/cache/claude-plugins-official/clangd-lsp/1.0.0/bin` that the interactive shell does not carry.

Every time one shell's invocation followed the other, Bazel saw a different `PATH` in the action environment, computed a different action key for every single C++ compile, and **invalidated the cache for the entire `rules_foreign_cc` CMake subtree**. That subtree is the expensive part — whisper.cpp + llama.cpp together account for ≈ 90 % of cold-build time.

Local disk cache was healthy (`/tmp/aegis-bazel-c48cf803db9a/action_cache/` persisted across invocations, `java.log` history spanned days), so BuildBuddy remote cache would NOT have helped — it mirrors the same action keys, and PATH differences would miss there too.

### Detection

Three misattributions before the real diagnosis:

1. **"USB drive mtime instability"** — plausible (repo lives on `/Volumes/Samsung PSSD T7 Media/aegis-core`), but `ls -la` on cache files showed timestamps consistent with last write, no mount/remount during the test window. Discarded.
2. **"rules_foreign_cc is non-hermetic, CMake sandbox paths bleed in"** — plausible, and genuinely true of rules_foreign_cc in general, but would not explain the magnitude observed (a fully-cold rebuild, not just re-link).
3. **"BuildBuddy remote cache will save us"** — assumed by reflex; ruled out once it was clear the local cache DID have the entries but the HASH lookups missed.

The actual diagnosis path:

1. `./tools/bazelisk/bazelisk build --nobuild //:app_local` (analyze-only) showed `0 total actions, Critical Path: 0.00s` — so the cache IS functioning when nothing invalidates.
2. `ps aux | grep clang` during a slow `bazel run` showed clang compiling `llama_cpp/common/arg.cpp` — confirmed rebuild, not re-link.
3. `./tools/bazelisk/bazelisk build //engine_cpp/cmd/engine:engine --explain=/tmp/aegis-build-explain.log --verbose_explanations` emitted, for every rebuilt action:
   ```
   'Compiling engine_cpp/src/inference/embedder.cc': 
     Effective client environment has changed. Now using
     PATH=/Users/bin.hsu/Library/Caches/bazelisk/...:/Users/bin.hsu/.claude/plugins/cache/claude-plugins-official/clangd-lsp/1.0.0/bin:...
   ```
   The `.claude/plugins/...` path entry was the smoking gun — it only exists in one of the two shells, and its presence/absence flips the PATH hash.

### Resolution

One-line addition to `.bazelrc`:

```
build --incompatible_strict_action_env
```

This flag normalizes the action environment to a static PATH that does not inherit from the caller's shell. Individual env vars we actually need (`DEVELOPER_DIR=/Library/Developer/CommandLineTools` for the macOS SDK lookup) are still explicitly propagated via `--action_env` elsewhere in the file, so the fix is strictly additive and does not break any existing hermetic-inputs assumption.

Verification: after one "last" full rebuild to seed the cache under the new stable PATH (4503 actions, ≈ 10 min), an immediate re-run produced:

```
INFO: Elapsed time: 0.969s, Critical Path: 0.02s
INFO: 1 process: 1 internal.
INFO: Build completed successfully, 1 total action
```

**600× speedup** on the no-op case, restoring Bazel's promised "fast incremental builds" to the LAN iteration cycle.

### Prevention

- The flag is now in `.bazelrc` as a `build`-scoped option; it applies to `bazel build`, `bazel run`, and `bazel test` uniformly and cannot be overridden accidentally by an interactive invocation.
- CI already inherited this behavior trivially (CI runners always start with a deterministic PATH), so no workflow change is needed.
- `--incompatible_strict_action_env` is the Bazel team's recommended posture for cross-environment caching and has been stable for multiple years; leaving it on is the canonical posture.

### Lessons

1. **"Cache miss" is not synonymous with "cache is broken."** Always ask whether the cache is being *asked* the right question. `--explain` is the tool that surfaces what Bazel thinks changed — it named the real culprit in its first line of output.
2. **A cross-environment build is implicitly a distributed build.** Even with one developer and one machine, *two shells that expose different PATHs* are two environments. Any tool that hashes the environment will treat them as distinct — tunnel-visioning on hermeticity inside the build graph misses the PATH leak between the graph and the caller.
3. **BuildBuddy / remote cache is not a fix for action-key instability.** It layers on top of the existing hash scheme. If local hashes are unstable, remote hashes are too. The right intervention is stabilizing the inputs, not adding another storage tier.
4. **Interactive-shell tooling (LSP helpers, plugin caches, etc.) legitimately mutate PATH and legitimately should not invalidate a build cache.** This is the mirror image of the rule: the tool's PATH edits are not semantically meaningful to the build; `--incompatible_strict_action_env` tells Bazel that correctly, once and for all.
5. **Three misattributions (§Detection) cost ~40 min before the diagnostic tool got used.** The reflex to fit a new symptom into a known failure mode (rules_foreign_cc is fragile, USB drives have mtime quirks, BuildBuddy fixes caching) delays the simpler step of asking Bazel directly. **Reach for `--explain` earlier.**

---

## Incident 14 — LAN transcript pipeline silently lost at the last hop (Phase-1 WS decoder stub)

**Date**: 2026-04-20  **Severity**: S3  **Duration**: ~3 hours (plus ≈ 2 hours burned on the wrong layers first — see §Detection)
**Related commit**: *this commit*

### Symptom

End-to-end LAN smoke (`bazel run //:app_local --with-frontend`) produced no transcript text on the Host page, despite:

- Whisper model loading cleanly (engine log showed `compute buffer (decode) = 95.91 MB`).
- WebRTC peer reaching `connected` in `chrome://webrtc-internals` (confirmed ICE candidate pair, `active=true` outbound-rtp, bytes flowing).
- Gateway receiving audio, forwarding to engine's `StreamTranscribe` stream.

Host UI permanently read `Waiting for the first segment…`. Speaking for a minute changed nothing.

### Root cause

Three stacked bugs, each hiding the next:

1. **Engine gRPC server had no keepalive policy**, and the gateway's client keepalive sends pings every 30 s (ADR-0006). gRPC C++ default rejects pings more frequent than 5 minutes → `ENHANCE_YOUR_CALM` / `too_many_pings` → GOAWAY → reconnect → engine reloads the whisper model from scratch → repeat every ≈ 4 minutes. Fixed by `GRPC_ARG_HTTP2_MIN_RECV_PING_INTERVAL_WITHOUT_DATA_MS=20000` + `GRPC_ARG_KEEPALIVE_PERMIT_WITHOUT_CALLS=1` on the C++ `ServerBuilder`.
2. **Engine's `Session::Run` was batch-transcribe-only**, not streaming. It accumulated PCM into a `std::vector<float>` for the entire session and called `engine->Transcribe(samples)` **once** after the stream closed (Stage 5 in `session.cc`). So live listeners never saw anything until EndMeeting, and even then the single segment was emitted on a stream that the gateway had already torn down → client missed it. Fixed by adding a live-window flush helper (`kLiveWindowSamples = 3 s × 16 kHz`) that calls `Transcribe` + `EmitTranscriptSegments` mid-loop, non-overlapping, with monotonic `segment_id` + derived `start_ms/end_ms`.
3. **`WebSocketTranscriptStreamProvider` was a Phase-1 stub that only handled string frames.** The gateway sends binary protobuf frames (`websocket.MessageBinary`, `proto.Marshal(ViewerEvent)`); the browser received the `ArrayBuffer`, checked `typeof ev.data === "string"`, saw `false`, and **silently discarded every frame**. The in-file comment literally said "Phase 2 decodes real protobuf" — never happened. Fixed by importing `@bufbuild/protobuf` + the generated `ViewerEvent` class, calling `ViewerEvent.fromBinary(new Uint8Array(ev.data))`, mapping proto enums / bigint fields back to the UI's light-weight `ViewerEvent` shape in `types.ts`, and calling `callbacks.onEvent`.

Bug 1 masked bug 2: with the stream dying every 4 minutes, nobody noticed `Session::Run`'s batch shape because the session never *ran* long enough to reach Stage 5. Bug 2 masked bug 3: with no segments emitted in the first place, nobody noticed the WS frames were being dropped by the browser. Observability gaps (the pipeline + webrtc Go packages had **zero** `slog` calls, and `runEgress` swallowed non-EOF errors with a bare `return`) made all three bugs equally invisible — the surface symptom was always the same: empty UI.

### Detection

Four wrong-layer probes before the real diagnosis:

1. **Chrome `webrtc-internals`** confirmed PC reached `connected`, bytes flowing — ruled out media layer.
2. **`chrome://webrtc-internals` stats graph** showed non-zero `bytesSent_in_bits/s` on speech — ruled out mic / codec negotiation.
3. **`nc -l 8888` + phone GET** showed LAN firewall was not blocking 8080 — ruled out macOS Application Firewall.
4. **Host UI UX audit** surfaced two unrelated UX gaps (React state not subscribing to PC `connectionState`, RAG dropdown showing 4 fake corpora) that delayed the real diagnosis because they looked plausible as primary bugs.

The first real signal came from `curl :8081/metrics | grep aegis_gateway`: `aegis_gateway_active_sessions=1` and `CreateMeeting ok=1`, but no counter for `StreamTranscribe` ingest beyond that — triggering the `grep -rn 'slog\.' gateway_go/internal/{pipeline,webrtc,grpc}/` that turned up **zero log statements** across the audio path. Adding `pipeline.engine.stream_opened`, `pipeline.engine.first_egress_message`, `pipeline.engine.stream_closed_{eof,err}`, and `pipeline.broadcast.transcript{,_no_subscribers}` (all Info, all carry `session_id`) made every layer of the bug sequentially visible in the next run:

- `ENHANCE_YOUR_CALM` GOAWAY appeared in `[gateway]` log → caught bug 1.
- After bug 1's fix, `first_egress_message` did not fire despite session running for 60 s → caught bug 2 (batch shape — `Session::Run` never reached Stage 5 while the client was subscribed).
- After bug 2's fix, `broadcast.transcript delivered=1 dropped=0` fired every 3 s but UI still empty → final layer, bug 3 (WS stub).

### Resolution

One commit, three logically-separable fixes:

- `engine_cpp/cmd/engine/main.cc` — two `AddChannelArgument` lines to permit the gateway's 30 s keepalive ping cadence.
- `engine_cpp/src/session/session.cc` — `flush_window()` helper called on every OPUS / PCM append; non-overlapping 3 s window emits mid-stream; Stage 5 becomes a force-flush of the trailing sub-window.
- `frontend_web/src/providers/TranscriptStreamProvider/WebSocketTranscriptStreamProvider.ts` — full binary-frame decoder via `@bufbuild/protobuf` `ViewerEvent.fromBinary`, mapped to UI types with `exactOptionalPropertyTypes`-safe conditional spread.

Plus the load-bearing observability layer that made the whole sequence diagnosable in one more run: `pipeline.go` / `negotiator.go` `slog` calls keyed on `session_id`, `runEgress` error path no longer silent, `Broadcast` return values used (`delivered, dropped`).

### Prevention

- **Every cross-process boundary carries `session_id` in its log context.** Future customer-support debugging on EKS will be `kubectl logs | grep session_id=...` across gateway + engine pods; this incident fixed the gateway side, engine-side C++ logger session-id tagging is a follow-up in ROADMAP Phase 4d.
- **`runEgress` error path now logs with the error text and the accumulated counters.** Any future engine-side failure — OOM, whisper crash, context cancel — surfaces as a `pipeline.engine.stream_closed_err` at Warn level with full context. No more bare `return`.
- **"TODO: Phase 2 decode real protobuf" is a lethal marker.** The file's TODO dated back to Phase 1 and was skipped over in subsequent reviews because nobody was exercising LAN-mode with real audio. Lesson in §Lessons below.

### Lessons

1. **Three misattributions cost ~2 h.** WebRTC-internals, firewall probe, React-state UI audit were each plausible *given the symptom* but each explored a layer that wasn't broken. The lesson from Incident 13 (reach for `--explain` earlier) applies here in a different form: when a pipeline stalls silently, **go add observability to every hop before debugging any one hop**. The `pipeline.engine.first_egress_message` log took 10 lines to add and resolved the first real ambiguity in one run.
2. **Silent `return` on error is worse than a panic.** `runEgress`'s pre-fix behavior (`if err == io.EOF { return }; return`) swallowed EVERY engine-side failure including context cancellation due to keepalive timeout. Had this emitted a Warn from day one, bug 1 would have been named in the first session. Enforce: **every error branch logs at Warn or higher with enough context to identify the session**.
3. **"Phase-N stub" markers accumulate risk.** `WebSocketTranscriptStreamProvider` carried its "decodes real protobuf in Phase 2" comment across 3 releases because the ws/viewer path wasn't exercised end-to-end until today. Two process implications: (a) PR reviews should flag any *stub* with no tracking issue, not just any *TODO*; (b) a first-user-exercise of a code path should be treated as a test, not a demo. A 10-line `ws-decode-smoke.test.ts` that sent a known ViewerEvent through the provider and asserted on the parsed shape would have caught this in Phase 1.
4. **Observability debt compounds non-linearly.** Pipeline + webrtc + grpc packages had 0 log statements each. Debugging the three-layer bug required *all three* packages to report what they saw; adding logs to one while leaving the others silent would just move the mystery. Paying the observability tax on a Go package is cheap and should not be deferred past "code is functional" — it should be a gate at "code is reviewable".
5. **End-to-end demo is the first real test of a LAN-mode stack.** Before today, `bazel run //:app_local` had never been driven with a human talking into a mic into the host UI until the transcript appeared. Every lower-level test (unit, integration) was stubbing out the layer above. The demo itself became the detector — acceptable for a solo project, **unacceptable at team scale**. ROADMAP Phase 4d's "Post-deploy E2E suite against staging" (Playwright happy-path) is the structural fix; that slice just gained a clear use-case from this incident.

---

## Incident 15 — `MODULE.bazel.lock` gitignored → cold /tmp cache cold-resolved new boringssl; empty BUILD

**Date**: 2026-04-21  **Severity**: S3  **Duration**: ~30 min (bounded — caught early while driving `lan-smoke.sh`)
**Related commit**: *this commit*

### Symptom

`./tools/scripts/lan-smoke.sh` step 3 (`bazel run //engine_cpp/cmd/engine -- seed ...`) aborted during Bazel analysis:

```
[bazelisk] repo path contains spaces; using cache at /tmp/aegis-bazel-c48cf803db9a
WARNING: For repository 'com_google_protobuf', the root module requires module
         version protobuf@28.2, but got protobuf@29.0 in the resolved dependency
         graph.
WARNING: For repository 'rules_cc',  requires rules_cc@0.0.17, but got rules_cc@0.1.5
WARNING: For repository 'rules_go',  requires rules_go@0.52.0, but got rules_go@0.59.0
ERROR: no such package '@@boringssl~//': BUILD file not found in directory ''
       of external repository @@boringssl~.
ERROR: Analysis of target '//engine_cpp/cmd/engine:engine' failed; build aborted
```

CI (`ci-baseline.yml` `bazel-unit-tests`) was green on the same `MODULE.bazel`, the same HEAD, within the preceding hour — so the failure was specific to the dev machine's local Bazel state, not any code under review.

### Root cause

Three conditions stacking:

1. **Path with spaces → `/tmp/` fallback cache**. `tools/bazelisk/bazelisk:42-48` routes `--output_user_root` to `/tmp/aegis-bazel-<hash>` when the repo path contains a space (the Rule-6-exception logic introduced for Incident 01's DottedVersion crash). Today was the first `bazel` invocation under this hash → fresh cold cache, nothing to restore.
2. **`MODULE.bazel.lock` was gitignored** (`.gitignore:58` from the Phase 0 governance-scaffolding commit `7ce3d1c`). The lock pins every transitive Bazel module version — without it in the repo, a fresh `output_user_root` must re-resolve from Bazel Central Registry from scratch.
3. **BCR tip pulls newer transitives than `MODULE.bazel` declares**. Our root module says `protobuf@28.2`, `rules_cc@0.0.17`, `rules_go@0.52.0`; BCR's newer transitive graph promoted them to 29.0 / 0.1.5 / 0.59.0. The downstream effect was boringssl's BCR module-shape change between those versions, producing an `@@boringssl~//` repo with an empty directory and no BUILD file.

**Why CI stayed green**: `ci-baseline.yml:137-154` uses `actions/cache` to persist `.bazel_cache/` across runs, keyed on `hashFiles('MODULE.bazel', '.bazelversion', '.bazelrc')`. Once any single CI run had resolved modules successfully (before today's BCR drift), every subsequent run restored the same resolved state and skipped the re-resolve. The dev box hitting a fresh `/tmp/` cache had no such safety net, and the cache key did not hash a lock (because one wasn't committed), so there was no signal to distinguish "old cache, still-valid" from "stale cache, should rebuild".

### Detection

Triggered by running `lan-smoke.sh` step 3 during the first LAN smoke drive of the feat/lan-smoke-ergonomics branch (PR #63). Signal chain:

1. The three `WARNING: ... requires X@vA, but got X@vB` lines in the log — these indicate version-override during resolution, which shouldn't happen on untouched `MODULE.bazel` unless the lock is absent or stale.
2. `ls .gitignore | grep MODULE.bazel.lock` — confirmed lock was gitignored.
3. `ls MODULE.bazel.lock` — confirmed a 131 KB lock existed on disk, dated Apr 20, from the previous in-repo `.bazel_cache/` resolution. It was the correct state; just not checked in.

### Resolution

One commit, four logically-grouped changes:

- `.gitignore` — removed the `MODULE.bazel.lock` entry, replacing it with a block comment explaining why the lock is now committed.
- `MODULE.bazel.lock` — committed the existing on-disk file (131 KB). Contents reflect the last successful BCR resolution from 2026-04-20.
- `.bazelrc` — explicit `common --lockfile_mode=update` (already the Bazel default, made visible in the config for discoverability).
- `.github/workflows/ci-baseline.yml` + `.github/workflows/release-staging-image.yml` — added `MODULE.bazel.lock` to the `hashFiles` list for each `actions/cache` key so the first post-merge CI run forces a fresh `.bazel_cache/` rebuild from the committed lock.

Contributors with a broken local cache (this dev machine) resolve by:

```bash
./tools/bazelisk/bazelisk clean --expunge
./tools/bazelisk/bazelisk build //...
```

### Prevention

- `MODULE.bazel.lock` is now the reproducibility source, matching Go `go.sum` and pnpm `pnpm-lock.yaml` stature. Bazel 7.x has significantly improved cross-platform lock portability (linux/darwin/windows share a canonical lock shape), eliminating the historical concern that motivated Phase 0's gitignore.
- Any future `MODULE.bazel` edit should land in the same commit as the resulting `MODULE.bazel.lock` diff. PR review should reject a `MODULE.bazel` change not accompanied by a lock change.
- CLAUDE.md Rule 9 pre-flight does not grow a check for this — lock is in the repo, fresh-clone flow just works.
- `--lockfile_mode=update` is retained for the next few weeks (tolerant of drift during the committed-lock transition); flipping CI to `--lockfile_mode=error` for strict reproducibility is a follow-up once we're confident no false positives remain.

### Lessons

1. **Gitignoring reproducibility files is the same class of mistake in any ecosystem.** Bazel's lock plays the same role as `go.sum` / `Cargo.lock` / `pnpm-lock.yaml`. When `.gitignore`'s Bazel section was authored in Phase 0, bzlmod was still maturing and the common wisdom had not caught up with Bazel 7.x's improved lock stability. Asymmetric cost: merge conflicts on a lock are 1-minute irritations to regenerate; version drift is hours of debugging once it happens, because the surface error (`empty BUILD`) lives four layers below the root cause.
2. **Cache-layer parity matters for reproducibility.** CI and local differed in exactly one variable — `output_user_root` location (persisted on CI, ephemeral on macOS-with-spaces dev boxes) — that nobody thought was semantically significant. Any reproducibility foundation that relies on "CI happens to persist cache across runs" is fragile: the moment a dev machine has a cold cache, the asymmetry surfaces as a "works on CI" bug.
3. **BCR drift is silent without a lock.** No error, no warning on a fresh clone — just a wall of `requested X, got Y` warnings that tired eyes tune out as "normal Bazel noise". A committed lock removes this class of drift entirely because every transitive version is pinned and diffed in code review.
4. **The `--test_output=short` preflight trap has a sibling here.** CLAUDE.md Rule 6's reminder that `--test_output=short` is invalid is a guard against silent flag ignore; the same category of preflight-gap applies to `--lockfile_mode`. The project would benefit from a "flags we commit to honoring" inventory that pre-commit could grep. Deferred — out of scope for this incident, but the lesson is logged.

---

## Incident 16 — `nightly-cognito-integration.yml` three-layer bug onion + cross-repo misdiagnosis

**Date**: 2026-04-25  **Severity**: S5  **Duration**: ~3 hours of dev-time noise (5:00 UTC failure observed at 9:00 Taipei session start; final green run 12:53 UTC)
**Related commits**: PR #95 (`ceb37f5` — layers 1+2) + PR #97 (`4041c79` — layer 3) + this commit (incident + ROADMAP tick)

### Symptom

The first scheduled run of `nightly-cognito-integration.yml` (Phase 4e-4 against the live Cognito pool `eu-central-1_0gdyxKxOB`) at 03:00 UTC failed at the `Read Cognito IDs from SSM Parameter Store` step:

```
/home/runner/work/_temp/<hash>.sh: line 11: AEGIS_COGNITO_APP_CLIENT_ID: unbound variable
##[error]Process completed with exit code 1.
```

The workflow had landed in [PR #88](https://github.com/BinHsu/aegis-core/pull/88) on 2026-04-24 and this was its first real run — the schedule trigger never fired before merge, and `workflow_dispatch` was not exercised pre-merge either.

### Root cause

Three independent latent bugs in the same step, each masking the next — a "bug onion" in debugging slang, where every fix peels off one layer and exposes the next:

1. **`set -e` does not propagate cleanly out of `$(...)` capture inside an `echo` argument.** The original step pattern was `echo "KEY=$(aws ssm get-parameter ...)" >> "$GITHUB_ENV"`. When the AWS CLI exits non-zero (parameter missing, KMS denied, etc.), the failure is dropped because the surrounding `echo` succeeds with whatever stdout the substitution produced — even an empty string. POSIX `set -e` semantics treat command-substitution failure inside an assignment-or-argument context as not-quite-fatal; modern bash 5.x preserves this gotcha.
2. **`>> $GITHUB_ENV` does NOT populate the current shell.** GitHub Actions reads the file written via `>> $GITHUB_ENV` between steps, so the var is visible to the **next** step but not to the **current** step. The original code then immediately referenced `$AEGIS_COGNITO_APP_CLIENT_ID` for `::add-mask::` in the same step — under `set -u` (`-o nounset`) this is a hard fail. Layer 2 was therefore guaranteed to fire even on a green AWS path; the workflow had never run cleanly to completion.
3. **`aws ssm get-parameter` without `--with-decryption` returns ciphertext for `SecureString`.** The Cognito SSM parameters are KMS-encrypted under `alias/aegis-staging-secrets` (the IAM contract block above the step grants `kms:Decrypt` on that alias — a tell that should have been read closer at workflow-authoring time). Without the flag the CLI returns the encrypted envelope, which is a non-empty string and therefore looks healthy to every shell-level check. Failure surfaces much later when AWS rejects the ciphertext as a User Pool ID with `AccessDeniedException` against a ciphertext-shaped resource ARN: `arn:aws:cognito-idp:eu-central-1:...:userpool/AQICAHg86Tae...` — the `AQICAHg86...` tail is a KMS envelope, not a real pool ID.

Layers 1+2 fired first because they happen before any AWS API call after the SSM read. Fixing them via PR #95 (capture into local var + check exit code + reference local var, not env) exposed layer 3 on the next manual dispatch.

A separate process failure also took ~30 min of dev-time noise: the morning's diagnosis pre-attributed layers 1+2's symptom to a teardown-wipe narrative ("ldz teardown destroyed `/aegis/staging/cognito/*` SSM PS") without checking AWS state. This produced three cross-repo comments on [ldz#153](https://github.com/BinHsu/aegis-aws-landing-zone/issues/153) (originally about Qdrant SSM PS wiped by Incident 33) asking ldz to extend their persistent-layer relocation scope to include Cognito. ldz's reply at 12:40 UTC contained `aws ssm describe-parameters` evidence showing all 5 Cognito SSM parameters had been intact the entire time, and that `staging/auth/` is baseline-tier (teardown-immune) since their PR #140. The misdiagnosis cost ldz one round of evidence-collection plus the issue reopen-and-re-close cycle.

### Detection

The failure surfaced as `unbound variable` rather than as the actual SSM-read or KMS-decrypt failure, because layer 2 fires before layer 3 has a chance to. This is what made the morning's misdiagnosis plausible: an unbound-variable error after three SSM reads strongly *looks* like one of the SSM reads failed silently and the variable was never set. The hypothesis fit the symptom.

What it didn't fit was the exit code path. Layer 1's bash gotcha means failure was always silent rather than loud, and the step ran for the same 5-second AWS-call duration whether or not the parameter existed. The morning's first triage tool was checking ldz's open cross-repo issues — finding [ldz#153](https://github.com/BinHsu/aegis-aws-landing-zone/issues/153) about SSM PS teardown-wipe (real, but Qdrant-scoped) and pattern-matching it onto the Cognito symptom. The faster path would have been `aws ssm describe-parameters --filters Key=Name,Option=BeginsWith,Values=/aegis/staging/cognito/` from the dev box (or asking ldz for that one query before drafting a scope-expansion request).

The actual correct diagnosis came in two beats:

1. PR #95 fixing layers 1+2 made the next manual run reach the bazel test step. The test then failed with `AccessDeniedException` against a ciphertext-shaped resource ARN — visibly KMS envelope, not a real `eu-central-1_<id>` shape.
2. The IAM contract comment at the top of the workflow grants `kms:Decrypt`. Reading that with the new symptom in mind — "why would we need KMS decrypt for plain `String` parameters?" — pointed at `--with-decryption` as the missing flag.

### Resolution

Two PRs landed sequentially:

- **PR #95** (`ceb37f5`) — fixed layers 1+2. Captured each `aws ssm get-parameter` result into a local variable, explicitly checked the CLI exit code, then `>> "$GITHUB_ENV"` AFTER the local references (`::add-mask::$CLIENT_ID`) had been resolved. Added a fail-loud annotation on read failure. Original annotation copy pre-attributed the failure to ldz teardown — that copy was rewritten in PR #97 to enumerate three neutral buckets (parameter destroyed / KMS denied / role scope gap) without prejudging.
- **PR #97** (`4041c79`) — added `--with-decryption` to the `aws ssm get-parameter` invocation. Single-flag change, ~5 lines including comment.

Verification: manual `gh workflow run nightly-cognito-integration.yml` after PR #97 merge → run [24931347407](https://github.com/BinHsu/aegis-core/actions/runs/24931347407) green; `TestOIDCIntegrationCognito` passed against the live pool end-to-end.

### Prevention

- **The fail-loud refactor in PR #95 stands.** Even though the original framing of the annotation was wrong, the structural shape (local var capture + explicit exit-code check + targeted annotation) is correct defense-in-depth for any future SSM-read regression — destroyed parameter, KMS-denied, role-scope mismatch, or any other ParameterNotFound-shaped surface. The annotation copy in PR #97 enumerates buckets without picking one.
- **Workflow-dispatch a new schedule-only workflow at least once before relying on the cron.** This was the load-bearing process gap. PR #88 introduced the workflow with a `schedule:` trigger; the first real run was the cron at 03:00 UTC the next day, by which time three latent bugs had compounded undetected. Adding a `workflow_dispatch:` block (already present here) is necessary but not sufficient — the discipline is to invoke it post-merge as part of the rollout, before any human depends on the cron's output.
- **`SecureString` parameters need `--with-decryption` at every read site.** The IAM contract comment that mentions `kms:Decrypt` is the leading indicator. A repo-level grep guard (`grep -r 'aws ssm get-parameter' .github/ | grep -v -- '--with-decryption'`) added to pre-commit would catch future regressions; deferred as a separate slice but logged here as the natural next step.
- **Verify before propagating cross-repo hypotheses.** When pattern-matching a local symptom onto a known cross-repo issue, run the cheapest direct-evidence query first (`aws ssm describe-parameters` in this case, ~5 seconds) before drafting an issue comment. The morning's three ldz comments would not have been written had the dev-side query been the first move.

### Lessons

1. **Onion bugs hide each other; the surface symptom is rarely the deepest cause.** Layer 2's `unbound variable` was a real bug, fixable on its own merits — but the underlying SSM read had never worked because of layer 1, and even if layers 1+2 were absent, layer 3 (decryption) would have produced its own `AccessDeniedException` later. Each fix exposes the next layer; treating the surface symptom as the only bug is the class error here.
2. **Pattern-matching a symptom onto an open cross-repo issue feels like efficiency but is actually a verification shortcut.** ldz#153 was about real teardown-wipe, just for a different parameter family. The morning's narrative connected the dots without checking whether the dots were actually connected. The cheapest direct-evidence query (`aws ssm describe-parameters`) is sub-second and bypasses the whole misdiagnosis chain. CLAUDE.md Rule 1 ("Self-Awareness & Honesty: Do NOT guess") and the `feedback_root_cause_first.md` memory both name this antipattern; this incident is the canonical example of how it surfaces under cross-repo coordination pressure.
3. **A new scheduled workflow is not "tested" until you `workflow_dispatch` it post-merge.** The schedule trigger is the production workload of a CI workflow; relying on it as the first-real-run path means latent bugs surface in the morning operator window with full coordination overhead. The discipline matches Rule 2's "load-bearing test must fail on broken code, pass on fixed" — for CI workflows specifically, that means at least one manual dispatch on `main` after merge.
4. **`SecureString` SSM PS are silent ciphertext leakers without `--with-decryption`.** The CLI's default behavior here is technically POLA-respecting (don't decrypt unless asked) but practically a foot-gun: ciphertext sails through every string-shape check and only fails far downstream. Anywhere `kms:Decrypt` appears in an IAM policy is a leading indicator that a SecureString read is in play; the corresponding code site should have `--with-decryption` and the lack of it is a static-checkable defect.
5. **Cross-repo coordination noise has a real cost on the other side.** ldz spent one round of evidence-gathering (`aws ssm describe-parameters` + IAM trust verification + ADR-028 §Out of scope reread) to refute a hypothesis we could have ruled out ourselves in 5 seconds. The "send the comment first, verify later" reflex is wrong even when both repos share a maintainer — the maintainer-cost of context-switching between repos to reply is real, and the politeness cost of asking for verification work that the asker could have done is real too. Future cross-repo asks should include "I've verified X locally" or "I cannot verify X locally because Y" as a leading line.

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
