// frontend_web/src/providers/AutoUpdateProvider/WebAutoUpdateProvider.ts
//
// Phase 3 web build has no native updater. Browsers ARE the updater:
// each reload picks up whatever Vite built + whatever headers the CDN
// served. So this implementation is a deliberate no-op that fails
// loudly rather than silently when `applyUpdate` is called — the
// caller probably shouldn't have surfaced an "Install update" button
// for a web deploy.

import type { AutoUpdateProvider, UpdateCheckResult } from "./types";

export class WebAutoUpdateProvider implements AutoUpdateProvider {
  async checkForUpdate(): Promise<UpdateCheckResult> {
    // Web cannot meaningfully compare its own bundle against a
    // "latest" — the CDN / browser cache has the final say. Report
    // up-to-date so any boot-time banner logic degrades to "no
    // banner shown" rather than a false positive.
    return { kind: "up-to-date" };
  }

  async applyUpdate(): Promise<void> {
    // A pure-web caller asking to "apply an update" is a bug — the
    // browser reload is the mechanism. Throw so a rogue UI path
    // surfaces in dev rather than silently succeeding.
    throw new Error(
      "WebAutoUpdateProvider.applyUpdate: not supported in a web-only " +
        "build. Trigger a browser reload (location.reload) or switch to " +
        "a Tauri shell where tauri-plugin-updater implements this path.",
    );
  }

  isSupported(): boolean {
    return false;
  }
}
