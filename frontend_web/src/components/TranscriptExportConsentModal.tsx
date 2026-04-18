// frontend_web/src/components/TranscriptExportConsentModal.tsx
//
// Phase 2 of the transcript consent flow per ADR-0024 Decision C.
// Shown before every transcript export; caller supplies user / session
// context and an `onConfirm` callback that receives the confirmed
// export-format choice. This component does NOT perform the export
// itself — Slice 5 wires the export action into the `onConfirm`
// callback. The audit record is emitted here.

import { useState, type JSX } from "react";

import { recordTranscriptExportConsent } from "@/lib/consent";

import { ConsentModal } from "./ConsentModal";

export type TranscriptExportFormat = "markdown" | "json";

export interface TranscriptExportConsentModalProps {
  readonly open: boolean;
  /** The signed-in user's stable identifier. */
  readonly userId: string;
  /** The active meeting's session identifier. */
  readonly sessionId: string;
  /** Invoked on confirm with the chosen export format. */
  readonly onConfirm: (format: TranscriptExportFormat) => void;
  /** Invoked on cancel / dismiss. */
  readonly onCancel: () => void;
}

export function TranscriptExportConsentModal(
  props: TranscriptExportConsentModalProps,
): JSX.Element {
  const { open, userId, sessionId, onConfirm, onCancel } = props;
  const [format, setFormat] = useState<TranscriptExportFormat>("markdown");

  const handleAccept = () => {
    recordTranscriptExportConsent(userId, sessionId, format);
    onConfirm(format);
  };

  return (
    <ConsentModal
      open={open}
      title="Export transcript — responsibility transfer"
      acceptLabel="Confirm export"
      declineLabel="Cancel"
      closable={true}
      onAccept={handleAccept}
      onDecline={onCancel}
    >
      <p>
        You are about to save the meeting transcript to a file. By proceeding,
        you confirm that:
      </p>
      <ul style={{ paddingLeft: "1.2rem", margin: "0.5rem 0 1rem" }}>
        <li>
          You are responsible for the transcript file under your jurisdiction's
          data-protection laws from this moment forward.
        </li>
        <li>
          You will not share the file with anyone not authorized to see this
          meeting's contents.
        </li>
        <li>
          This action is recorded in the consent ledger with your user ID,
          session ID, timestamp, and client metadata.
        </li>
      </ul>
      <fieldset
        style={{
          border: "1px solid #ddd",
          padding: "0.6rem",
          margin: "0.5rem 0",
        }}
      >
        <legend style={{ padding: "0 0.4rem", fontSize: "0.85rem" }}>
          Export format
        </legend>
        <label style={{ marginRight: "1rem" }}>
          <input
            type="radio"
            name="export-format"
            value="markdown"
            checked={format === "markdown"}
            onChange={() => setFormat("markdown")}
          />{" "}
          Markdown (<code>.md</code>)
        </label>
        <label>
          <input
            type="radio"
            name="export-format"
            value="json"
            checked={format === "json"}
            onChange={() => setFormat("json")}
          />{" "}
          JSON
        </label>
      </fieldset>
    </ConsentModal>
  );
}
