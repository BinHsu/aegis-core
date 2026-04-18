// frontend_web/src/providers/AutoUpdateProvider/index.ts
//
// Public surface of the AutoUpdateProvider port. Re-exports types
// and the factory. Phase 4 Tauri wire-up is a one-line change here.

import { WebAutoUpdateProvider } from "./WebAutoUpdateProvider";
import type { AutoUpdateProvider } from "./types";

export type {
  AutoUpdateProvider,
  UpdateCheckResult,
  UpdateInfo,
} from "./types";

export { WebAutoUpdateProvider };

export function pickAutoUpdateProvider(): AutoUpdateProvider {
  return new WebAutoUpdateProvider();
}
