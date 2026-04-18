// frontend_web/src/providers/FileSystemProvider/index.ts
//
// Public surface of the FileSystemProvider port. Re-exports types and
// the factory; app code never imports `WebFileSystemProvider`
// directly so the Phase 4 Tauri swap is a one-line change here.

import { WebFileSystemProvider } from "./WebFileSystemProvider";
import type { FileSystemProvider } from "./types";

export type {
  FileExportFormat,
  FileSystemProvider,
  SaveAsRequest,
  SaveAsResult,
} from "./types";

export { WebFileSystemProvider };

/**
 * Build the `FileSystemProvider` for the current deploy target.
 *
 * Phase 3 web is the only option. When Phase 4 ships the Tauri shell,
 * this factory gains a `VITE_AEGIS_SHELL` env branch (or detects the
 * Tauri-injected global) and returns a `TauriFileSystemProvider`.
 */
export function pickFileSystemProvider(): FileSystemProvider {
  return new WebFileSystemProvider();
}
