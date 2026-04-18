// frontend_web/src/providers/NotificationProvider/index.ts
//
// Public surface of the NotificationProvider port. Re-exports types
// and the factory. Concrete implementations stay hidden behind the
// port so the Phase 4 Tauri swap is a one-line change here.

import { WebNotificationProvider } from "./WebNotificationProvider";
import type { NotificationProvider } from "./types";

export type {
  NotificationPermissionState,
  NotificationProvider,
  NotificationRequest,
  NotificationSeverity,
} from "./types";

export { WebNotificationProvider };

export function pickNotificationProvider(): NotificationProvider {
  return new WebNotificationProvider();
}
