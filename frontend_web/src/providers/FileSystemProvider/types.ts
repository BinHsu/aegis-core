// frontend_web/src/providers/FileSystemProvider/types.ts
//
// ADR-0002 Constraint 5: filesystem operations must sit behind a
// provider interface so the Phase 4+ Tauri shell can later implement
// them via its native Rust backend without touching call sites.
//
// The Phase 3 web implementation targets the "save this transcript"
// flow (export Markdown / JSON from the Host UI). It uses the
// standard Blob + anchor download trick — no `chrome.*`, no File
// System Access API (which WKWebView does not ship), no
// `navigator.serviceWorker`. The caller does not need to know any of
// that; it just calls `saveAs(...)`.
//
// Phase 4 Tauri replaces the web impl with a binding that calls
// Rust's `rfd::FileDialog` + `fs::write` — same public surface.

/**
 * Supported export formats. Kept as a closed union so the switch
 * statements that branch on it at the consumer side (setting
 * Content-Type / default extension) stay exhaustive.
 */
export type FileExportFormat = "markdown" | "json" | "text";

/**
 * Input shape for a "save as …" prompt. The file contents live in
 * memory; for the multi-megabyte-transcript edge case Phase 4 Tauri
 * will add a streaming variant — Phase 3 web does not need it because
 * a 4-hour meeting transcript is a few hundred KB of UTF-8.
 */
export interface SaveAsRequest {
  /** Suggested filename including extension (e.g. "meeting-2026-04-17.md"). */
  readonly suggestedName: string;
  /** In-memory content to write. UTF-8 for text formats; caller serializes. */
  readonly content: string;
  /** Format hint — drives the Blob's MIME type and the suggested extension
   *  when the OS picker asks. */
  readonly format: FileExportFormat;
}

/**
 * Outcome of a save attempt. `cancelled` = user dismissed the picker;
 * `written` = the browser (or Tauri) confirmed the write. Web's
 * Blob-download trick cannot distinguish "user hit Save" from "user
 * hit Cancel" in the browser's download prompt, so the Web impl
 * always reports `written` once the anchor click has dispatched —
 * an acceptable lie because the worst case is the user sees a
 * "saved" toast when they chose Cancel (harmless compared to the
 * inverse).
 */
export type SaveAsResult =
  | { readonly kind: "written"; readonly finalName: string }
  | { readonly kind: "cancelled" };

/**
 * The port. Everything else the Host UI needs from the filesystem
 * (open an existing corpus file, tail a log) will grow here rather
 * than spreading into ad-hoc Blob helpers across components.
 */
export interface FileSystemProvider {
  /**
   * Prompt the user to save the supplied content under the suggested
   * name. Resolves when the operation is observably complete (write
   * finished on Tauri; download dispatched on Web).
   */
  saveAs(req: SaveAsRequest): Promise<SaveAsResult>;

  /**
   * True when the provider can produce a user-visible save dialog at
   * all. Web is always `true` (Blob download works in every target
   * browser). Tauri will be `true` after the shell's permissions
   * grant a save path; an unsandboxed fallback may report `false`.
   */
  isSupported(): boolean;
}
