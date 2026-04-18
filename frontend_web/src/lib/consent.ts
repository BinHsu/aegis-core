// frontend_web/src/lib/consent.ts
//
// Consent-record persistence + audit-log emission helpers per
// ADR-0024 (Consent flows — audio-processing + transcript 2-phase).
//
// Phase 3 demo horizon: audio-processing consent is localStorage-
// persisted per policy version; transcript-export consent fires
// an audit record via `console.info` and the future Phase 4 wiring
// drops in a gateway `LogConsentEvent` RPC at the same emit-point.
// The consent-record SHAPE is stable across the Phase 3 → Phase 4
// migration so no caller code changes when the wire path moves.

/**
 * Privacy-policy version. Bump when the copy in
 * `AudioProcessingConsent.tsx` materially changes; bumping invalidates
 * previously-stored acceptance records (the key changes from
 * `aegis.consent.audio_processing.v1` to `…v2`), forcing the user to
 * consent to the new terms.
 */
export const CURRENT_POLICY_VERSION = "v1" as const;
export type PolicyVersion = typeof CURRENT_POLICY_VERSION;

const AUDIO_CONSENT_KEY_PREFIX = "aegis.consent.audio_processing.";

/**
 * A consent record written at acceptance time. Shape is load-bearing
 * across the Phase 3 → Phase 4 migration: the same structure is what
 * the Phase 4 gateway's DynamoDB consent-ledger row will accept.
 */
export interface AudioProcessingConsentRecord {
  readonly kind: "consent:audio_processing:accepted";
  readonly userId: string;
  readonly policyVersion: PolicyVersion;
  readonly acceptedAt: string; // ISO 8601 UTC
  readonly clientMetadata: {
    readonly userAgent: string;
    readonly deployMode: string;
  };
}

export interface TranscriptExportConsentRecord {
  readonly kind: "consent:transcript:export:confirmed";
  readonly userId: string;
  readonly sessionId: string;
  readonly exportFormat: "markdown" | "json";
  readonly exportedAt: string; // ISO 8601 UTC
  readonly clientMetadata: {
    readonly userAgent: string;
    readonly deployMode: string;
  };
}

function clientMetadata(): {
  readonly userAgent: string;
  readonly deployMode: string;
} {
  return {
    userAgent:
      typeof navigator !== "undefined" ? navigator.userAgent : "unknown",
    deployMode:
      (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] as string | undefined) ??
      "local",
  };
}

/**
 * Check whether the current user has already accepted the audio-
 * processing consent at the current policy version. Cheap, synchronous,
 * safe to call from a React effect.
 */
export function hasAudioProcessingConsent(
  userId: string,
  policyVersion: PolicyVersion = CURRENT_POLICY_VERSION,
): boolean {
  if (typeof localStorage === "undefined") return false;
  const key = AUDIO_CONSENT_KEY_PREFIX + policyVersion;
  const raw = localStorage.getItem(key);
  if (raw === null) return false;
  try {
    const parsed = JSON.parse(raw) as AudioProcessingConsentRecord;
    return parsed.userId === userId && parsed.policyVersion === policyVersion;
  } catch {
    // Corrupt record — treat as absent; the user re-accepts.
    return false;
  }
}

/**
 * Record audio-processing consent acceptance for the current user.
 * Writes to localStorage (Phase 3) and emits via `console.info` so the
 * Phase 4 gateway RPC drop-in sees a compatible record shape.
 */
export function recordAudioProcessingConsent(
  userId: string,
  policyVersion: PolicyVersion = CURRENT_POLICY_VERSION,
): AudioProcessingConsentRecord {
  const record: AudioProcessingConsentRecord = {
    kind: "consent:audio_processing:accepted",
    userId,
    policyVersion,
    acceptedAt: new Date().toISOString(),
    clientMetadata: clientMetadata(),
  };
  if (typeof localStorage !== "undefined") {
    const key = AUDIO_CONSENT_KEY_PREFIX + policyVersion;
    try {
      localStorage.setItem(key, JSON.stringify(record));
    } catch {
      // Quota / private-mode storage failure: surface via the audit
      // emit below but do not throw — the user has still consented
      // for this session; the localStorage gate will re-ask next
      // visit, which is acceptable.
    }
  }
  emitConsentEvent(record);
  return record;
}

/**
 * Record transcript-export consent confirmation. Slice 5 wires this
 * into the actual export button; Slice 3 ships the helper so the
 * export flow has one place to call.
 */
export function recordTranscriptExportConsent(
  userId: string,
  sessionId: string,
  exportFormat: "markdown" | "json",
): TranscriptExportConsentRecord {
  const record: TranscriptExportConsentRecord = {
    kind: "consent:transcript:export:confirmed",
    userId,
    sessionId,
    exportFormat,
    exportedAt: new Date().toISOString(),
    clientMetadata: clientMetadata(),
  };
  emitConsentEvent(record);
  return record;
}

/**
 * Internal audit emit. Phase 3: `console.info`. Phase 4: replace with
 * a gateway `LogConsentEvent` RPC call; the record shape is stable so
 * no call-site churn.
 */
function emitConsentEvent(
  record: AudioProcessingConsentRecord | TranscriptExportConsentRecord,
): void {
  // eslint-disable-next-line no-console
  console.info("[aegis.consent]", record);
}
