// frontend_web/src/providers/NotificationProvider/types.ts
//
// ADR-0002 Constraint 5: desktop notifications must sit behind a
// provider interface so Phase 4 Tauri can call the OS notification
// center natively (tauri-plugin-notification) while Phase 3 web uses
// the standard `Notification` Web API.
//
// Aegis uses notifications for chief-of-staff-relevant events the user
// would otherwise miss when the browser tab is not focused: "meeting
// participant joined", "host audio dropped", "transcription fell
// behind", etc. The provider does NOT handle in-page toasts (those
// are a pure React-component concern); it handles cross-tab /
// out-of-focus OS-level surfaces.
//
// Notifications are always permission-gated and always dismissible;
// the provider returns whatever the platform offers and the caller
// degrades gracefully when the permission is absent.

/**
 * Platform permission state. Matches the `NotificationPermission`
 * DOM type (so callers branching on either get the same three
 * values) plus an `unsupported` variant covering WebViews that don't
 * expose the Notification API at all.
 */
export type NotificationPermissionState =
  | "granted"
  | "denied"
  | "default"
  | "unsupported";

/**
 * Severity hint. Drives the default OS icon / sound on Tauri and
 * maps to a notification style on the Web (Web does not expose
 * severity directly — the hint is metadata for UI analytics and for
 * matching the Tauri surface).
 */
export type NotificationSeverity = "info" | "warning" | "error";

export interface NotificationRequest {
  readonly title: string;
  readonly body?: string;
  readonly severity?: NotificationSeverity;
  /**
   * Monotonic-ish identifier a caller can reuse to dedupe repeat
   * notifications — e.g. "ingest-stall" emitted every 30s while the
   * condition persists should not spam the OS with twelve copies. Web
   * passes this as `Notification.tag`; Tauri will use it as a
   * replacement key.
   */
  readonly tag?: string;
}

/**
 * The port. `show()` is explicitly fire-and-forget — notifications
 * are a one-way signal; the caller never blocks on user interaction.
 * If click-through actions become necessary (Phase 5+?), they will
 * grow here as an additional return type, not as a breaking change.
 */
export interface NotificationProvider {
  /**
   * Current permission state. Cheap, synchronous, polled at every
   * notify call — no caching, so a mid-session permission change
   * (rare but possible in Web) takes effect immediately.
   */
  getPermission(): NotificationPermissionState;

  /**
   * Request permission from the user. Web opens the browser's
   * permission prompt; Tauri is a no-op if the app's installer
   * already granted notification rights, or defers to the OS
   * notification center otherwise.
   *
   * Resolves with the post-prompt state. Callers MAY show this
   * behind a user-gesture (click) to satisfy browsers that only
   * honor permission prompts in user-activation contexts.
   */
  requestPermission(): Promise<NotificationPermissionState>;

  /**
   * Dispatch a notification if permission allows. Silently no-ops
   * when permission is `denied` or `unsupported`; callers should
   * not conditionalize on the permission state — that's the
   * provider's job. The `default` case shows nothing and returns
   * false (caller may choose to prompt for permission at that
   * moment).
   *
   * Returns whether the notification was dispatched.
   */
  show(req: NotificationRequest): boolean;

  /**
   * True when the platform has any form of notification UI
   * available. Web returns `"Notification" in window`; Tauri will
   * always be true on supported OSes (macOS / Windows / Linux
   * D-Bus).
   */
  isSupported(): boolean;
}
