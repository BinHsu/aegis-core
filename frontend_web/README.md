# frontend_web — Aegis Core pure-web host + viewer UI

Phase 1 C1 scaffold. Vite + React 19 + TypeScript, pure web per
[ADR-0002](../docs/adr/0002-desktop-shell-technology.md) Constraint 2
and [ADR-0003](../docs/adr/0003-host-audio-capture-strategy.md).

## Layout

```
frontend_web/
├── package.json
├── vite.config.ts
├── tsconfig.json
├── index.html
├── src/
│   ├── main.tsx              # router entry
│   ├── App.tsx               # shell layout
│   ├── pages/
│   │   ├── Host/             # staff UI (audio capture + transcript display)
│   │   └── Viewer/           # boss UI (transcript + prompter hints)
│   ├── providers/            # ADR-0002 Constraint 2 abstractions
│   │   ├── AudioCaptureProvider/
│   │   ├── TranscriptStreamProvider/
│   │   ├── AuthProvider/
│   │   ├── FileSystemProvider/
│   │   └── NotificationProvider/
│   ├── components/
│   ├── hooks/
│   ├── lib/
│   └── styles/
└── tests/
```

## Development

Use the `tools/scripts/frontend.sh` wrapper — it drives a hermetic Node 20 + pnpm
managed by `aspect_rules_js` (ADR-0015). No system `node` or `npm` required.

```bash
# First-time setup (hermetic pnpm install via Bazel-managed Node)
./tools/scripts/frontend.sh install

# Dev server with HMR on :5173
./tools/scripts/frontend.sh dev

# Type-check only (CI-friendly, no build)
./tools/scripts/frontend.sh typecheck

# Production build → frontend_web/dist/
./tools/scripts/frontend.sh build
```

## Bazel integration

The frontend is wrapped under `aspect_rules_js` as of Phase 3a (ADR-0015). The
`tools/scripts/frontend.sh` script invokes pnpm through the Bazel-managed
Node toolchain — the same Node version that CI uses. Local `node_modules/` is
populated by this script, not by a system npm or nvm. See ADR-0015 for the
hermetic-Node rationale and the `rules_oci` packaging wiring for the CloudFront
deploy path.

## Provider interfaces (ADR-0002 Constraint 2)

Five provider abstractions isolate platform-specific behavior so the
Tauri wrap in Phase 4+ can swap implementations without touching UI:

| Provider                    | Web implementation (Phase 1)              | Tauri implementation (Phase 4+)     |
| --------------------------- | ----------------------------------------- | ----------------------------------- |
| `AudioCaptureProvider`      | `getUserMedia` + `getDisplayMedia` + Web Audio | CoreAudio (macOS) / WASAPI (Win) |
| `TranscriptStreamProvider`  | gRPC-Web (Cloud) / WebSocket (Local)      | Same — transport is portable        |
| `AuthProvider`              | Cognito OAuth via redirect                | Tauri secure storage + OAuth        |
| `FileSystemProvider`        | File System Access API + download fallback| Tauri native dialogs                |
| `NotificationProvider`      | Notification API                          | Tauri native notifications          |

All call sites go through the provider; direct `navigator.*` is
forbidden by ADR-0002 Constraint 2.
