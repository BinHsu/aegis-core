// frontend_web/src/components/AudioProcessingConsent.tsx
//
// The ARCH §9.3 / ADR-0024 Decision A one-time audio-processing
// consent gate. Mount on the signed-in Host page; checks localStorage
// on every render; prompts if no acceptance record exists for the
// current user at the current policy version. No decline path —
// consent is required to use the app, so refusing just leaves the
// modal open (user can close the tab).

import { useEffect, useState, type JSX } from "react";

import {
  CURRENT_POLICY_VERSION,
  hasAudioProcessingConsent,
  recordAudioProcessingConsent,
} from "@/lib/consent";

import { ConsentModal } from "./ConsentModal";

export interface AudioProcessingConsentProps {
  /** The signed-in user's stable identifier. */
  readonly userId: string;
  /**
   * Optional callback invoked once after consent has been recorded.
   * Useful if the parent wants to show a toast / kick off a post-
   * consent action. Not required; the modal closes itself.
   */
  readonly onAccepted?: () => void;
}

export function AudioProcessingConsent(
  props: AudioProcessingConsentProps,
): JSX.Element | null {
  const { userId, onAccepted } = props;
  // Lazy initial state: synchronous localStorage read on mount so the
  // modal never flashes for users who already accepted.
  const [needed, setNeeded] = useState<boolean>(
    () => !hasAudioProcessingConsent(userId, CURRENT_POLICY_VERSION),
  );

  // Re-check when the userId changes (sign-out → sign-in as a
  // different account). The consent is per-user so the new principal
  // must grant fresh.
  useEffect(() => {
    setNeeded(!hasAudioProcessingConsent(userId, CURRENT_POLICY_VERSION));
  }, [userId]);

  const handleAccept = () => {
    recordAudioProcessingConsent(userId, CURRENT_POLICY_VERSION);
    setNeeded(false);
    onAccepted?.();
  };

  return (
    <ConsentModal
      open={needed}
      title="Audio processing consent"
      acceptLabel="Agree"
      closable={false}
      onAccept={handleAccept}
    >
      {/*
        ARCH §9.3 copy verbatim. When this prose materially changes,
        bump CURRENT_POLICY_VERSION in lib/consent.ts so existing
        users re-accept the new terms.
      */}
      <p>
        Aegis transcribes your meeting audio in real time and generates
        suggestion hints from a knowledge corpus you select. Audio is processed
        only in memory and is never saved.
      </p>
      <p style={{ fontSize: "0.85rem", color: "#666" }}>
        Your acceptance is recorded with your user identifier, policy version (
        <code>{CURRENT_POLICY_VERSION}</code>), the current timestamp, and your
        browser identifier. See ARCHITECTURE.md §9.3 and ADR-0024 for details on
        the consent ledger.
      </p>
    </ConsentModal>
  );
}
