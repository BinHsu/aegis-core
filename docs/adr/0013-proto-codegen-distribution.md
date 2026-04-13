# ADR-0013: Proto Codegen Distribution Strategy

- **Status**: Accepted
- **Date**: 2026-04-13
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core has **two active build systems** touching the same
`proto/aegis/v1/aegis.proto` contract:

1. **Bazel** (authoritative for artifacts). `//proto/aegis/v1:aegis_go_proto`
   and `//proto/aegis/v1:aegis_cc_grpc` produce generated code on demand
   into `bazel-bin/` during a build. These outputs live only inside the
   Bazel sandbox and are not visible to any other tool.
2. **Go toolchain** (authoritative for developer experience). `go build`,
   `go mod tidy`, and every IDE driven by `gopls` discover packages by
   reading physical source files under the module root (`gateway_go/`).
   They cannot see Bazel's sandboxed outputs.

Phase 2 Session A1 (Gateway → Engine gRPC client) tripped this
conflict head-on. Adding a `require google.golang.org/grpc ...` to
`gateway_go/go.mod` got silently reverted by `go mod tidy` because no
`.go` file actually imported it — and the Go file that *would* import
it (`import "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"`) could
not resolve that import path without a physical `.pb.go` file on
disk. Chicken and egg.

We need a deliberate strategy for where generated proto code lives
so that **both** build systems see the same API surface. This ADR
captures the decision before Phase 2 scales up the number of Go (and
later TypeScript) proto consumers.

## Decision Drivers

- **D1. Developer experience.** `gopls` autocomplete, `go build`, and
  IDE navigation must work without launching Bazel. A developer
  opening `gateway_go/cmd/gateway/main.go` in VS Code and getting
  red underlines on `aegis.v1.EngineClient` is a daily-friction tax
  we should not pay.
- **D2. Bazel remains authoritative for builds.** Production artifacts
  (OCI images in Phase 4, signed release binaries) must be produced
  by Bazel from `.proto` directly — never by trusting a hand-edited
  `.pb.go`. The answer to "where does this generated symbol come
  from?" must always be "the .proto file, via Bazel."
- **D3. No global tool installs.** CLAUDE.md Rule 6. A `buf generate`
  invocation must not require the developer to `brew install buf`.
- **D4. Drift detection.** If the checked-in `.pb.go` diverges from
  what `.proto` would produce, CI must fail loudly. Drift is the
  failure mode this ADR creates; the mitigation is non-negotiable.
- **D5. Scales to TypeScript.** Phase 3 frontend consumes the same
  contracts via `protobuf-ts` / `@bufbuild/protobuf-es`. The
  distribution strategy should extend cleanly to that target.

## Considered Options

### Option α — Bazel-only; no checked-in generated code

All `.pb.go` / `.pb.h` / `.ts` files live solely in `bazel-bin/`
ephemerally. Developers must `bazel build //proto/...` before
working on consumer code; IDEs need special configuration (e.g.,
`gopackagesdriver` for gopls) to see Bazel outputs.

- **Pros**: single source of truth; zero drift possible; strongest
  hermeticity story.
- **Cons**: violates D1 severely. gopls `gopackagesdriver` is an
  experimental integration that routinely breaks across Go and
  Bazel version combinations. Every contributor needs editor
  configuration, not just a build tool configuration. No sustained
  portfolio-quality OSS project in 2025–2026 ships a pure-Bazel
  Go proto setup for exactly this reason.

### Option β — Checked-in generated code under a tracked directory; Bazel as authoritative producer ✅ CHOSEN

Generated `.pb.go` files are committed to the repository under a
dedicated tree (`gateway_go/gen/go/...`). A contributor running
`./tools/scripts/proto_gen.sh` re-generates these files via `buf
generate` using remote plugins (so no protoc-gen-go install).
Bazel's `go_proto_library` continues to produce the same Go types
from `.proto` at build time — **Bazel does not consume the
checked-in files**; it ignores them because no `BUILD.bazel`
references the `gen/go/` tree. A CI drift check runs
`./tools/scripts/proto_gen.sh && git diff --exit-code` and fails
the build if anything is out of sync. This is the pattern used by
`grpc-go`, Kubernetes, Istio, and virtually every major polyglot
OSS Go+proto project.

- **Pros**: D1 fully satisfied (IDE works without special setup);
  D2 preserved (Bazel still generates; checked-in files are read-only
  artifacts); D3 satisfied (buf wrapper is hermetic per §Tool
  Wrapping below); D4 satisfied by CI check; D5 extends naturally
  (Phase 3 adds `frontend_web/gen/ts/...` outputs).
- **Cons**: generated artifacts in git diff noise on `.proto`
  changes. Reviewers learn to skim generated files. The first-time
  cost of the drift-check tooling. Small but real.

### Option γ — Generate into a dedicated Go module published as its own repo

Create `github.com/BinHsu/aegis-proto-go` as a separate OSS repo that
only contains the generated code; consumers import it via `go get`.
The main repo drops it into the module graph via `replace` directives
during development.

- **Pros**: clean separation of generated vs handwritten;
  external consumers can adopt the proto contracts without cloning
  Aegis.
- **Cons**: Phase 1 has no external consumers. Two repos to
  synchronize and tag. Replace directives break on `go get`.
  Premature at this phase. Revisit when external consumers exist.

## Decision Outcome

**We adopt Option β.** Generated Go proto code lives under
`gateway_go/gen/go/<package-path>/*.pb.go`, is committed to the
repository, and is regenerated via `./tools/scripts/proto_gen.sh`
whenever `.proto` changes. Bazel continues to be the authoritative
producer at build time.

### Layout

```
proto/aegis/v1/aegis.proto          # source of truth
buf.yaml                            # buf module config (lint, break, format)
buf.gen.yaml                        # buf codegen config (plugins + outputs)

gateway_go/
├── go.mod                          # module github.com/BinHsu/aegis-core/gateway_go
├── go.sum
└── gen/
    └── go/
        └── aegis/
            └── v1/
                ├── aegis.pb.go     # message types (protoc-gen-go)
                └── aegis_grpc.pb.go   # service stubs (protoc-gen-go-grpc)

tools/
├── buf/
│   └── buf                         # bazelisk-style wrapper for buf CLI
└── scripts/
    └── proto_gen.sh                # invokes buf generate from repo root
```

Phase 3 adds an analogous `frontend_web/gen/ts/aegis/v1/*` tree for
the TypeScript consumers (using `@bufbuild/protobuf-es` or
equivalent via `buf generate`).

### Tool Wrapping

Following the `tools/bazelisk/bazelisk` pattern:

- `tools/buf/buf` — shell script that downloads the pinned `buf`
  CLI binary (`v1.67.0` matching `.pre-commit-config.yaml`) into
  `.bufsk/` (gitignored) on first use and execs it. No system
  install. Supports macOS ARM64 / AMD64 and Linux ARM64 / AMD64.
- `tools/scripts/proto_gen.sh` — thin caller: `cd $REPO_ROOT && buf
  generate`. buf reads `buf.gen.yaml` and dispatches to **remote
  plugins** (`buf.build/protocolbuffers/go`, `buf.build/grpc/go`)
  so no local `protoc-gen-go` install is needed.

### CI Drift Check

A new GitHub Actions job in `.github/workflows/ci-baseline.yml`:

```yaml
proto-codegen-drift:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - run: ./tools/scripts/proto_gen.sh
    - run: git diff --exit-code -- gateway_go/gen/ frontend_web/gen/
```

If a PR changed `.proto` but forgot to re-run `proto_gen.sh`, this
job fails with the specific file diff, and the fix is a one-liner
for the contributor.

### Developer Workflow

```bash
# Editing the proto
$EDITOR proto/aegis/v1/aegis.proto

# Regenerate downstream Go (and Phase 3+ TypeScript) artifacts
./tools/scripts/proto_gen.sh

# Sync go.mod / go.sum (picks up new/removed dependencies from the
# generated imports)
cd gateway_go && ../tools/scripts/go.sh mod tidy

# Commit together — the CI drift check enforces that .proto changes
# and generated changes land in the SAME PR.
git add proto/aegis/v1/aegis.proto \
        gateway_go/gen/go/... \
        gateway_go/go.mod gateway_go/go.sum
git commit -m "feat(proto): ..."
```

## Consequences

### Positive

- gopls / IDE works out of the box; contributor onboarding cost drops
  to zero for Go editor experience.
- Go developers without Bazel context can still read and navigate
  generated code — useful for portfolio consumption.
- `go mod tidy` behavior becomes predictable (dependencies are
  "real" because imports are "real").
- Phase 3 TypeScript codegen slots in naturally under the same
  pattern (`frontend_web/gen/ts/...`).
- The drift-check CI job is a small, targeted investment that catches
  an entire class of mistake.

### Negative

- Generated files in `git log` / `git diff`. Reviewers skim them.
  PR review discipline: ignore `gen/` changes unless the diff is
  suspicious compared to the `.proto` change.
- Repo clone is a bit larger. For our single `aegis.v1` package this
  is ~80 KB, not meaningful.
- A contributor can commit `.proto` without re-running
  `proto_gen.sh` and get a red CI. This is the intended failure
  mode but does produce a round-trip.
- Two codepaths (Bazel-internal vs checked-in) produce the "same"
  Go package. A subtle version drift between `protoc-gen-go`
  versions used in Bazel (`rules_go` built-in) vs buf
  (`buf.build/protocolbuffers/go`) could theoretically yield
  different output. Mitigation: pin both to the same upstream
  version and re-validate on each upgrade.

### Risks

- **protoc-gen-go remote plugin drift.** buf remote plugins track
  upstream; pin them by explicit version in `buf.gen.yaml`
  (`buf.build/protocolbuffers/go:v1.36.5`) and bump deliberately.
- **Security implications of remote codegen.** buf remote plugins
  execute server-side on BSR. They are SOC 2-audited but if the
  threat model ever includes "BSR compromise injects Go code into
  our repo," revisit. A fallback is Docker plugins using pinned
  image digests, which we can swap in without changing the rest of
  the flow.

## Related

- ADR-0008 Monorepo Folder Structure — `gen/go/` fits as a
  documented subtree under `gateway_go/`.
- ADR-0009 C++ Build and Toolchain — C++ codegen is handled
  entirely by Bazel's `cc_proto_library`; this ADR does not change
  that. C++ consumers are Bazel-native and do not need checked-in
  headers.
- `.pre-commit-config.yaml` — buf lint / breaking hooks will be
  supplemented by the drift-check CI step, not by a pre-commit
  hook (a hook that regenerates files breaks the "hooks only
  check" convention).
- `buf.yaml` — module config (lint / break). `buf.gen.yaml` is a
  separate file in the same directory.
