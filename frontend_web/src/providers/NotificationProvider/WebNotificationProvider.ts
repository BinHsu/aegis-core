// frontend_web/src/providers/NotificationProvider/WebNotificationProvider.ts
//
// Phase 3 web implementation: the standard `Notification` Web API.
// No `chrome.*`, no Service Worker push (ADR-0002 Constraint 3; push
// notifications would require a Service Worker + a push subscription
// endpoint neither of which Aegis ships at this tier). Phase 4 Tauri
// will replace this with `@tauri-apps/plugin-notification`.

import type {
  NotificationPermissionState,
  NotificationProvider,
  NotificationRequest,
} from "./types";

export class WebNotificationProvider implements NotificationProvider {
  getPermission(): NotificationPermissionState {
    if (typeof Notification === "undefined") {
      return "unsupported";
    }
    // Notification.permission returns "granted" | "denied" | "default".
    return Notification.permission;
  }

  async requestPermission(): Promise<NotificationPermissionState> {
    if (typeof Notification === "undefined") {
      return "unsupported";
    }
    // Safari ≥ 16 and every other target WebView ship the
    // Promise-returning form; ADR-0002 already documents Firefox /
    // Safari as unsupported for the host role, so the old callback-
    // only fallback is not worth carrying.
    return Notification.requestPermission();
  }

  show(req: NotificationRequest): boolean {
    const perm = this.getPermission();
    if (perm !== "granted") {
      return false;
    }
    // Build NotificationOptions with conditional-spread because
    // tsconfig's `exactOptionalPropertyTypes: true` forbids passing
    // `undefined` explicitly — absence and `undefined` are distinct.
    const opts: NotificationOptions = {
      // Web Notification spec has no first-class severity; `silent`
      // approximates "info" so error-severity notifications still
      // beep. Tauri will map the severity directly when it lands.
      silent: req.severity === "info",
      ...(req.body !== undefined ? { body: req.body } : {}),
      ...(req.tag !== undefined ? { tag: req.tag } : {}),
    };
    try {
      new Notification(req.title, opts);
      return true;
    } catch {
      // Some WebViews throw when the Notification constructor is
      // called from a non-user-activation context. Swallow — the
      // caller sees a `false` return and can fall back to an
      // in-page toast.
      return false;
    }
  }

  isSupported(): boolean {
    return typeof Notification !== "undefined";
  }
}
