// frontend_web/src/providers/FileSystemProvider/WebFileSystemProvider.ts
//
// Phase 3 web implementation: Blob + anchor-click download. Works in
// every target WebView (Chrome, Edge, WKWebView); no `chrome.*`, no
// File System Access API (WKWebView gap), no Service Worker (ADR-0002
// Constraint 3). Phase 4 Tauri will replace this with native file
// dialog + fs::write while keeping the `FileSystemProvider` surface
// unchanged.

import type {
  FileExportFormat,
  FileSystemProvider,
  SaveAsRequest,
  SaveAsResult,
} from "./types";

const MIME_BY_FORMAT: Record<FileExportFormat, string> = {
  markdown: "text/markdown;charset=utf-8",
  json: "application/json;charset=utf-8",
  text: "text/plain;charset=utf-8",
};

export class WebFileSystemProvider implements FileSystemProvider {
  async saveAs(req: SaveAsRequest): Promise<SaveAsResult> {
    const mime = MIME_BY_FORMAT[req.format];
    const blob = new Blob([req.content], { type: mime });
    const url = URL.createObjectURL(blob);
    try {
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = req.suggestedName;
      // Must be attached to the DOM for Firefox to honor the click —
      // Chrome and WKWebView tolerate a detached anchor but doing
      // both costs nothing and avoids a whole class of browser quirk.
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      // See types.ts SaveAsResult docstring: Web cannot observe the
      // user's response to the browser download prompt. We report
      // `written` optimistically; the caller's UX should treat this
      // as "save dispatched" rather than "confirmed on disk".
      return { kind: "written", finalName: req.suggestedName };
    } finally {
      // Release the blob after the click handler has definitely
      // consumed the URL. A tick is plenty; Chrome documents that
      // same-task reads complete before revoke takes effect.
      queueMicrotask(() => URL.revokeObjectURL(url));
    }
  }

  isSupported(): boolean {
    // Blob + anchor-download is universally available in our targets.
    return typeof document !== "undefined" && typeof Blob !== "undefined";
  }
}
