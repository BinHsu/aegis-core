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

```bash
# First-time setup
cd frontend_web
npm install          # or pnpm install

# Dev server with HMR
npm run dev
# → Local: http://localhost:5173/

# Type-check only (CI-friendly, no build)
npm run typecheck

# Production build
npm run build        # outputs dist/
npm run preview      # serve dist/ locally
```

## Bazel integration — **deferred to Phase 4**

Unlike `engine_cpp/` and `gateway_go/`, the frontend is currently
NPM-managed, not Bazel-wrapped. Reasoning:

- Frontend tooling (Vite, React, Next, Bun) evolves rapidly; pinning
  it into Bazel creates constant version drift.
- The frontend's only production artifact is a static bundle — there
  is no downstream Bazel consumer of its build outputs until Phase 4a
  OCI packaging.
- `aspect_rules_js` + `rules_nodejs` + `rules_ts` bzlmod setup is
  non-trivial and its value here is low until the packaging story
  matters.

When Phase 4a lands, we wrap this directory with `aspect_rules_js` to
produce a deterministic Bazel artifact that `rules_oci` can embed.

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
