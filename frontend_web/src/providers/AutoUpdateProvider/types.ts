// frontend_web/src/providers/AutoUpdateProvider/types.ts
//
// ADR-0002 Constraint 5: auto-update behaviour must sit behind a
// provider interface so the Phase 4+ Tauri shell can plug in Tauri's
// updater plugin (tauri-plugin-updater) without refactoring the
// Host UI's "check for updates" affordance.
//
// Pure-web builds have no meaningful auto-update — the browser is
// the delivery mechanism; each reload IS the "update". The Web
// implementation below therefore always reports "up to date" and
// `applyUpdate` is a no-op. The interface still exists so future
// Tauri / PWA-install flows slot in without changing consumer code.

export interface UpdateInfo {
  /** Semver of the available update (e.g. "0.5.3"). */
  readonly version: string;
  /** Optional user-facing changelog summary to show in the UI. */
  readonly notes?: string;
  /**
   * True when applying the update requires restarting the app /
   * shell. Web apps never need a restart; Tauri bundles do.
   */
  readonly requiresRestart: boolean;
}

export type UpdateCheckResult =
  | { readonly kind: "up-to-date" }
  | { readonly kind: "update-available"; readonly info: UpdateInfo }
  | { readonly kind: "check-failed"; readonly reason: string };

/**
 * The port. Two callers are expected:
 *
 *   - An app-boot probe that decides whether to display an
 *     "Update available" banner in the header.
 *   - A user-initiated "Check for updates…" menu item.
 *
 * Both paths call `checkForUpdate()` and branch on the result; the
 * UI never needs to know whether the backing implementation is web,
 * Tauri, or a future PWA flow.
 */
export interface AutoUpdateProvider {
  /**
   * Ask the backing mechanism whether a newer version exists.
   * Resolves with one of the three `UpdateCheckResult` variants.
   * Web returns `up-to-date` unconditionally (no shell version to
   * compare against). Tauri queries the configured update endpoint
   * and surfaces real semver + notes.
   */
  checkForUpdate(): Promise<UpdateCheckResult>;

  /**
   * Begin applying an available update. Rejects when the provider
   * has no update to apply (e.g. Web, or Tauri after an up-to-date
   * check). The Tauri implementation downloads + installs + restarts
   * the app; caller should assume the page will unload mid-call.
   */
  applyUpdate(): Promise<void>;

  /**
   * True when the platform has a real auto-update mechanism. Web is
   * always false (the browser is the updater). Tauri is true only
   * when the updater plugin is configured.
   */
  isSupported(): boolean;
}
