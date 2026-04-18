// frontend_web/src/components/ConsentModal.tsx
//
// Reusable modal shell for the consent flows defined in ADR-0024.
// Intentionally bare-bones visually — the copy is what matters. Three
// buttons: primary accept, optional secondary decline, optional
// dismiss-by-close (set via `closable`).
//
// Uses a <dialog> element with the native modal behavior (showModal /
// close), which gets us ESC-to-close + focus-trap + backdrop click for
// free across every target WebView (Chrome / Edge / WKWebView per
// ADR-0002 Constraint 4). No library dependency.

import { useEffect, useRef, type JSX, type ReactNode } from "react";

export interface ConsentModalProps {
  /** Controls whether the dialog is open. */
  readonly open: boolean;
  /** Title rendered at the top of the modal. */
  readonly title: string;
  /** Rich body — caller assembles prose, links, bullet points. */
  readonly children: ReactNode;
  /** Label for the primary accept button (e.g. "Agree", "Confirm export"). */
  readonly acceptLabel: string;
  /** Optional decline / cancel button. Omit to force accept-only flow. */
  readonly declineLabel?: string;
  /** Invoked when the user clicks the primary accept button. */
  readonly onAccept: () => void;
  /** Invoked on decline click or (if `closable`) on dismiss. */
  readonly onDecline?: () => void;
  /**
   * When true, ESC key and backdrop click dismiss the modal (calling
   * `onDecline` if provided). When false, the user can only progress
   * by clicking an explicit button — the pattern for the
   * audio-processing one-time gate which has no decline path.
   */
  readonly closable?: boolean;
}

export function ConsentModal(props: ConsentModalProps): JSX.Element | null {
  const {
    open,
    title,
    children,
    acceptLabel,
    declineLabel,
    onAccept,
    onDecline,
    closable = true,
  } = props;
  const dialogRef = useRef<HTMLDialogElement | null>(null);

  // Drive the underlying <dialog> element's imperative open/close from
  // the `open` prop. Using showModal() rather than the `open` attribute
  // so we get focus-trap + scroll-lock + backdrop.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    if (open && !dialog.open) {
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  // When `closable` is false, intercept ESC + backdrop click. The
  // <dialog> element's default cancel event is what ESC triggers.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) return;
    const onCancel = (e: Event) => {
      if (!closable) {
        e.preventDefault();
        return;
      }
      onDecline?.();
    };
    dialog.addEventListener("cancel", onCancel);
    return () => dialog.removeEventListener("cancel", onCancel);
  }, [closable, onDecline]);

  if (!open) {
    // Keep the dialog element mounted so refs stay stable; the effect
    // above drives its open/close state.
  }

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby="consent-modal-title"
      style={{
        border: "1px solid #ccc",
        borderRadius: "8px",
        padding: "1.5rem",
        maxWidth: "540px",
        fontFamily: "system-ui, sans-serif",
        lineHeight: 1.5,
      }}
    >
      <h2
        id="consent-modal-title"
        style={{ marginTop: 0, marginBottom: "1rem", fontSize: "1.1rem" }}
      >
        {title}
      </h2>
      <div style={{ marginBottom: "1.25rem", fontSize: "0.95rem" }}>
        {children}
      </div>
      <div
        style={{ display: "flex", gap: "0.5rem", justifyContent: "flex-end" }}
      >
        {declineLabel !== undefined ? (
          <button
            type="button"
            onClick={() => onDecline?.()}
            style={{
              padding: "0.4rem 0.9rem",
              border: "1px solid #ccc",
              borderRadius: "4px",
              background: "white",
              cursor: "pointer",
            }}
          >
            {declineLabel}
          </button>
        ) : null}
        <button
          type="button"
          onClick={onAccept}
          autoFocus
          style={{
            padding: "0.4rem 0.9rem",
            border: "1px solid #0d6efd",
            borderRadius: "4px",
            background: "#0d6efd",
            color: "white",
            cursor: "pointer",
          }}
        >
          {acceptLabel}
        </button>
      </div>
    </dialog>
  );
}
