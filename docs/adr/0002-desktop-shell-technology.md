# ADR-0002: Desktop Shell Technology — Tauri over Qt / Electron

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —
- **Applies to**: Phase 4+ implementation (MVP ships as pure web; see
  ADR-0003 Host Audio Capture Strategy)

## Context

Aegis Core needs, eventually, a desktop client that can access OS-level audio
APIs (CoreAudio on macOS, WASAPI on Windows) for scenarios where the pure web
client's `getUserMedia` / `getDisplayMedia` are not sufficient — specifically
when the staff's counterparty uses a native desktop meeting application (Zoom
Desktop, Teams Desktop) whose audio does not flow through a capturable
browser tab.

The MVP (Phase 1–4) is shipped as **pure web only**. A native desktop shell
becomes relevant in **Phase 4 or later**, once "native meeting app support"
has been validated as a real customer need.

Even though implementation is deferred, we document the shell technology
choice now, because:

1. The Phase 3 web frontend must be built in a way that can later be wrapped
   in the chosen shell without major refactoring. The wrong frontend
   assumptions today create expensive rework tomorrow.
2. Preventing the Phase 3 team from accidentally depending on
   browser-only APIs (e.g., Chrome-extension-specific features, APIs not
   present in `WKWebView`) preserves the Tauri option.
3. The project owner has already evaluated the trade-offs and wants the
   reasoning captured before onboarding new contributors, so the decision
   does not have to be re-litigated every time someone new looks at the
   stack.

## Decision Drivers

- **D1. "Clone it, build it, it just works" philosophy.** Inherited from V1
  (see `README.md`). A hard constraint on distribution size and installation
  friction.
- **D2. Simple UI surface.** Aegis's client shows a rolling-window prompter,
  a few buttons (New Meeting / End / RAG selector), and a meeting status
  indicator. No complex widgets, no drag-drop, no waveform visualization,
  no dockable panels, no MDI.
- **D3. Phase 3 web frontend reuse.** Phase 3 already delivers a React + Vite
  web client. The desktop shell should wrap that same codebase rather than
  require parallel implementation in another UI framework.
- **D4. Security-sensitive client.** The client processes voice audio and
  prompter content. Memory safety at the shell layer reduces a class of
  exploitation vectors that would otherwise rely on manual discipline to
  avoid.
- **D5. Licensing clarity for future commercial distribution.** The project
  may be offered under commercial terms in future enterprise deals. Permissive
  licenses (MIT / Apache 2.0) are preferred over LGPL / commercial dual
  licensing.
- **D6. Team skill profile.** The team has existing C++ expertise, but that
  expertise is targeted at the inference engine, not GUI. There is no
  in-house Qt expertise. Web frontend skills are available.
- **D7. Binary size budget.** In line with D1, the total desktop install
  footprint should stay under ~20 MB if at all possible.

## Considered Options

### Option A — Tauri (Web UI + Rust backend) ✅ chosen

A lightweight shell that runs the frontend in the OS's built-in WebView
(WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux) and exposes a
Rust backend for OS-level operations. The frontend is the same HTML / CSS /
JS codebase as the web version.

- Binary size: 3–15 MB typical.
- Memory baseline: ~80 MB per app.
- License: MIT / Apache 2.0 dual.
- UI layer: whatever web framework the team already uses (React, Svelte,
  Vue, Solid). No Rust in the UI layer.
- Native layer: Rust, with Tauri's IPC bridge for `invoke`/event calls.
- Maturity: Tauri v2 (2024 GA) is production-ready for Aegis's feature
  surface. Some ecosystem gaps remain for advanced scenarios (complex file
  pickers, printing, deep shell integration).

### Option B — Qt / QML (C++ native)

A mature native GUI framework widely used in commercial desktop software
(OBS Studio, VLC, Autodesk Maya, KDE Plasma, Telegram Desktop).

- Binary size: 30–80 MB for typical Qt deployments (heavily dependent on
  which Qt modules are linked and whether static or dynamic linking is
  used).
- Memory baseline: ~100 MB per app.
- License: LGPLv3 (free, with dynamic-linking constraints) or commercial
  (paid, per-developer).
- UI layer: Qt Widgets (classic, mature) or Qt Quick / QML (modern,
  declarative, binding-oriented).
- Native layer: same C++ process; direct access to OS APIs via Qt's cross-
  platform abstractions or platform-specific headers.
- Maturity: extremely mature; 25+ years of development, rich widget
  ecosystem, commercial support available from The Qt Company.

### Option C — Electron (Web UI + Node.js backend)

The most widely deployed web-shell framework (VS Code, Slack, Discord,
Figma Desktop, 1Password). Bundles a full Chromium and Node.js runtime with
every application.

- Binary size: 120–200 MB typical.
- Memory baseline: ~300 MB per app (often more with multiple renderer
  processes).
- License: MIT.
- UI layer: any web framework.
- Native layer: Node.js (JavaScript / TypeScript), plus optional native
  node modules for lower-level OS access.
- Maturity: extremely mature; dominant in the web-shell space.

### Option D — Native per-platform (SwiftUI + WinUI 3 + GTK)

Build three separate native applications, one per platform, each using the
platform's native UI framework.

- Binary size: smallest overall (each app is single-platform and uses
  system frameworks).
- License: none (each framework is owned by its OS vendor).
- UI layer: SwiftUI on macOS, WinUI 3 / XAML on Windows, GTK4 / libadwaita
  on Linux.
- Native layer: Swift / C# / C, respectively.
- Maturity: all three are mature native frameworks.

## Decision Outcome

**We choose Option A (Tauri).**

The decision applies to Phase 4 or whenever a native desktop shell becomes
necessary. Until then, Phase 3 ships as pure web, and the Phase 3 frontend
must be written with Tauri-compatibility in mind (see
"Constraints on Phase 3 Web Frontend" below).

### Why Tauri

- **D1 wins decisively.** A ~10 MB Tauri binary (vs ~50 MB Qt, ~180 MB
  Electron) fits the "clone and run" ethos. It is the only framework where a
  casual user can download the binary over a mediocre network without
  noticing it.
- **D2 matches Tauri's sweet spot.** Aegis's UI is exactly the kind of
  "information display with minor interaction" app Tauri was designed for.
  Every capability Qt offers over Tauri (complex widgets, Qt Charts,
  QGraphicsView, docking frameworks, etc.) is unused by Aegis.
- **D3 is the biggest single win.** The Phase 3 web frontend becomes the
  Phase 4 desktop frontend with near-zero changes. No parallel UI
  implementation, no feature-lag between web and desktop, no divergence
  over time. This is the largest productivity multiplier of the four
  options.
- **D4 is satisfied.** Rust's memory safety covers the native bridge layer
  (audio capture, IPC, filesystem). The attack surface added by the shell
  is minimal compared to a C++ equivalent.
- **D5 is satisfied.** MIT / Apache 2.0 dual license is the cleanest stance
  for any future commercial offering.
- **D6 is workable.** The team's Rust investment is small — the Tauri Rust
  layer in Aegis will be ~200–500 lines (audio bridge, menu handlers,
  filesystem access, auto-update). Most client code remains TypeScript /
  React.
- **D7 is exceeded** (target <20 MB, Tauri delivers ~10 MB).

### Why Not Qt

- **Violates D1 and D7 significantly.** 30–80 MB binaries are acceptable
  for traditional desktop software but materially degrade the "minimal
  struggle" promise that V2 inherits from V1.
- **Violates D3 severely.** Phase 3 web work would not transfer to a Qt
  shell at all; the UI would need to be re-implemented in QML or Qt
  Widgets. This roughly doubles UI engineering work and creates two
  codebases to keep in sync.
- **Violates D6.** Qt has a steep learning curve, especially QML's binding
  model and Qt's meta-object system. Without in-house Qt expertise,
  onboarding cost is real and risky for a small team.
- **Creates D5 friction.** LGPL is free for dynamic linking but imposes
  constraints on distribution and static linking. Commercial Qt licenses
  are not cheap. Acceptable, but adds legal review overhead.
- **Over-delivers on D2.** Qt's widget richness is impressive but wasted on
  Aegis's simple UI.
- Qt's genuine advantages (mature native widgets, superior complex-UI
  performance, commercial support, embedded targets) are not needs Aegis
  has in the current or Phase 5 roadmap.

**Qt would become the right choice if**:

- Aegis's product scope expands to include complex visualizations
  (waveforms, timeline scrubbing, multi-meeting dashboards, audio editing).
- A senior Qt engineer joins the team and brings their own velocity.
- An embedded / kiosk deployment target becomes a real requirement (Qt for
  Embedded has no equivalent in the other options).

None of these are on the current or Phase 5 roadmap. If any become real,
this ADR should be revisited.

### Why Not Electron

- **Violates D1 fatally.** 120–200 MB binary plus ~300 MB RAM baseline is
  the antithesis of "clone and run minimal struggle." A staff member
  running Aegis alongside their meeting client (Zoom, Chrome with many
  tabs) on an 8 GB MacBook Air would immediately feel the pressure.
- **Violates D7 by an order of magnitude.**
- **Partially offsets D3.** Like Tauri, Electron would allow reuse of the
  web frontend. But this single advantage does not outweigh the size and
  memory cost.
- Electron is the right choice for applications where the desktop
  footprint is dwarfed by other concerns (VS Code, Slack) or where deep
  Chromium features are required (extension APIs, specific devtools
  integrations). Aegis has neither condition.

### Why Not Native per-platform

- **Violates D3 completely.** Three separate UI implementations; no reuse
  of the Phase 3 web client at all.
- **Violates D6.** The team would need Swift, C# / XAML, and GTK / C
  knowledge simultaneously.
- **Violates D1 indirectly** — the "build it locally" promise requires a
  single Bazel build pipeline. Three native toolchains per platform break
  this cleanly-hermetic story.
- **Maintenance burden**: every product change lands three times.
- Native per-platform is the right choice for applications that need the
  absolute best platform integration (Apple flagship apps, Microsoft
  Office). Aegis does not prioritize that level of polish.

## Consequences

### Positive

- Smallest binary size of all viable options.
- Zero-duplication UI: Phase 3 web work becomes Phase 4 desktop work.
- Memory safety at the shell layer.
- Clean MIT / Apache 2.0 licensing, no legal review overhead.
- Low onboarding cost for contributors with web backgrounds.
- Consistent frontend between web and desktop; features ship in one place.

### Negative

- **Small Rust investment required.** Phase 4 team members will need
  working knowledge of Rust (cargo, ownership, `tokio` async basics). The
  Rust surface in Aegis is small, but not zero.
- **OS-WebView inconsistencies.** WebView2, WKWebView, and WebKitGTK have
  subtle behavioral differences (CSS rendering quirks, IndexedDB quotas,
  media API support, `SharedArrayBuffer` availability). The Phase 3 web
  team must test across WebViews early, not just on Chrome.
- **Ecosystem immaturity.** Tauri v2 is solid, but the Rust crate
  ecosystem surrounding it is thinner than Qt's. Some things (complex file
  dialogs, printer access, deep system integrations) may require writing
  more glue code than Qt would.
- **Deferred decision risk.** By documenting this choice before
  implementation, we commit Phase 3 to Tauri-compatible patterns. If Phase
  4 re-evaluation reveals Tauri is the wrong call (e.g., a Tauri security
  issue with no fix, or the ecosystem stalls), some Phase 3 decisions may
  need revisiting. This is an acceptable risk given the alternatives.

## Constraints on the Phase 3 Web Frontend

To preserve the Tauri-wrap path, Phase 3 MUST follow these rules. Breaking
any of them creates rework cost for Phase 4.

1. **No Chrome-extension-only APIs.** Avoid the `chrome.*` namespace,
   Manifest V3 features, and any API that only works in Chrome packaged
   contexts.
2. **Audio capture behind an abstraction.** `getUserMedia` and
   `getDisplayMedia` calls must be wrapped in an `AudioCaptureProvider`
   interface that can later be swapped for a Tauri IPC-backed native
   implementation. The rest of the frontend must never call
   `navigator.mediaDevices` directly.
3. **No reliance on Service Workers for core functionality.** WebView
   Service Worker support varies. Offline cache, PWA install prompts, and
   push notifications are fine as progressive enhancements for the web-only
   MVP, but the core prompter UI must work without them.
4. **Avoid APIs unavailable in WKWebView.** Notably: `SharedArrayBuffer`
   (requires cross-origin isolation headers that WKWebView handles
   differently), `WebCodecs` on older macOS versions, and some advanced
   WebRTC features. Test early on a macOS machine against WKWebView, not
   just against Chrome DevTools.
5. **Filesystem, notification, and auto-update concerns behind providers.**
   Each must be reachable via a provider interface so the Tauri backend
   can later implement it via Tauri's native APIs without refactoring the
   call sites.
6. **No browser-specific persistent storage assumptions.** Phase 3 can use
   `localStorage` / `IndexedDB` freely, but expect storage quotas to
   differ across WebViews. Do not store large binaries (model files,
   audio) client-side.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers
- ADR-0003 Host Audio Capture Strategy
- `ARCHITECTURE.md` §1 System Topology (Tauri / Rust reference)
- `ARCHITECTURE.md` §5 Dual-Mode Parity (Local vs Cloud)
- `README.md` — "Clone it, build it, it just works" ethos
