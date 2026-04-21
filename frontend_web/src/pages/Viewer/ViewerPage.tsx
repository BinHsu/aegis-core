// frontend_web/src/pages/Viewer/ViewerPage.tsx
//
// Phase 1 C4. Boss / observer UI:
//   - parse session_id + token from URL (ADR-0001 Option B)
//   - subscribe via TranscriptStreamProvider (transport picked at
//     build time per ADR-0007 deploy mode)
//   - render rolling 5-line prompter window per ARCH §4 step 9
//   - show "Host reconnecting..." on transient state changes
//   - NO export, NO history (L3, L4 — intentional features)

import { useEffect, useMemo, useState, type JSX } from "react";
import { useParams, useSearchParams } from "react-router-dom";

import {
  type HintUrgency,
  type MeetingState,
  pickTranscriptStreamProvider,
  type ViewerEvent,
} from "@/providers/TranscriptStreamProvider";
import { hintStyleForUrgency } from "@/lib/hintStyling";

const PROMPTER_WINDOW = 5;

// Deploy mode + endpoint are normally injected at build time via Vite
// env vars. The default for ENDPOINT falls back to same-host:8080
// (not hardcoded `localhost`) so the LAN-scan-QR-code viewer flow
// works out of the box — a phone loading this page from
// `http://192.168.x.y:5173/view/...` automatically points at
// `http://192.168.x.y:8080` for the gateway.
const DEPLOY_MODE = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
  | "cloud"
  | "local";
const ENDPOINT =
  import.meta.env["VITE_AEGIS_GATEWAY_ENDPOINT"] ??
  (typeof window !== "undefined"
    ? `${window.location.protocol}//${window.location.hostname}:8080`
    : "http://localhost:8080");

export function ViewerPage(): JSX.Element {
  const { sessionId } = useParams<{ sessionId: string }>();
  const [searchParams] = useSearchParams();
  const token = searchParams.get("token");

  const [state, setState] = useState<MeetingState>("active");
  const [stateReason, setStateReason] = useState<string | null>(null);
  const [transcript, setTranscript] = useState<
    {
      readonly text: string;
      readonly speaker: string;
      readonly isQuestion: boolean;
    }[]
  >([]);
  const [hint, setHint] = useState<{
    readonly suggestion: string;
    readonly urgency: HintUrgency;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const provider = useMemo(
    () =>
      pickTranscriptStreamProvider({
        deployMode: DEPLOY_MODE,
        endpoint: ENDPOINT,
      }),
    [],
  );

  useEffect(() => {
    if (!sessionId || !token) return;

    const onEvent = (event: ViewerEvent): void => {
      switch (event.kind) {
        case "transcript":
          setTranscript((prev) => {
            const next = [
              ...prev,
              {
                text: event.text,
                speaker: event.speakerLabel,
                isQuestion: event.isQuestion,
              },
            ];
            return next.slice(-PROMPTER_WINDOW);
          });
          break;
        case "hint":
          // Rationale is intentionally NOT surfaced on the viewer side —
          // it's a staff-internal field (why the hint was generated /
          // which corpus chunk cited) and has no value to the room.
          // See proto/aegis/v1/aegis.proto `SendOfficerHintRequest.rationale`.
          setHint({ suggestion: event.suggestion, urgency: event.urgency });
          break;
        case "state":
          setState(event.state);
          setStateReason(event.reason ?? null);
          break;
      }
    };
    const onError = (e: Error): void => setError(e.message);
    const onClose = (reason?: string): void => {
      setStateReason(reason ?? "stream closed");
    };

    const sub = provider.subscribe(
      { sessionId, viewerToken: token },
      { onEvent, onError, onClose },
    );
    return () => sub.unsubscribe();
  }, [provider, sessionId, token]);

  if (!sessionId || !token) {
    return (
      <main>
        <h2>Viewer</h2>
        <p style={{ color: "#c0392b" }}>
          Missing <code>session_id</code> or <code>token</code> in URL. Check
          the invite link the host shared with you (ADR-0001 Option B).
        </p>
      </main>
    );
  }

  return (
    <main>
      <h2>Viewer</h2>
      <p style={{ color: "#888", fontSize: "0.85rem" }}>
        Session <code>{sessionId}</code> — transport <code>{DEPLOY_MODE}</code>{" "}
        via <code>{ENDPOINT}</code>
      </p>

      {state === "host-reconnecting" && (
        <div
          style={{
            background: "#fff3cd",
            border: "1px solid #ffeeba",
            padding: "0.5rem 1rem",
            margin: "1rem 0",
          }}
        >
          <strong>Host reconnecting…</strong>
          {stateReason && (
            <span style={{ marginLeft: "0.5rem", fontSize: "0.85rem" }}>
              ({stateReason})
            </span>
          )}
        </div>
      )}

      {state === "ended" && (
        <div
          style={{
            background: "#f8d7da",
            border: "1px solid #f5c6cb",
            padding: "0.5rem 1rem",
            margin: "1rem 0",
          }}
        >
          <strong>Meeting ended.</strong>
          {stateReason && (
            <span style={{ marginLeft: "0.5rem", fontSize: "0.85rem" }}>
              {stateReason}
            </span>
          )}
        </div>
      )}

      <section
        style={{
          minHeight: "12rem",
          padding: "1rem",
          border: "1px solid #ddd",
          borderRadius: "4px",
          marginBottom: "1rem",
          fontFamily: "Georgia, serif",
          fontSize: "1.1rem",
          lineHeight: 1.6,
        }}
        aria-label="Live transcript (rolling window)"
      >
        {transcript.length === 0 ? (
          <p style={{ color: "#bbb" }}>Waiting for live transcript…</p>
        ) : (
          transcript.map((line, idx) => (
            <p
              key={idx}
              style={{
                margin: "0.25rem 0",
                fontWeight: line.isQuestion ? "bold" : "normal",
              }}
            >
              <span style={{ color: "#888", marginRight: "0.5rem" }}>
                {line.speaker}:
              </span>
              {line.text}
            </p>
          ))
        )}
      </section>

      {hint && <HintDisplay hint={hint} onDismiss={() => setHint(null)} />}

      {error && (
        <p style={{ color: "#c0392b" }}>
          <strong>Stream error:</strong> {error}
        </p>
      )}

      <p style={{ color: "#888", fontSize: "0.75rem", marginTop: "2rem" }}>
        Late joiners deliberately see no history — only segments produced after
        you joined. This is a privacy feature, not a bug (ARCH §11 L4).
      </p>
    </main>
  );
}

/**
 * Renders a single hint with urgency-differentiated styling. LOW/NORMAL
 * render inline (below the transcript, no dismiss). HIGH/URGENT render
 * as a pinned banner above the transcript with an urgency label and a
 * dismiss button so the viewer can clear a stale alert manually.
 *
 * Visual logic lives in `@/lib/hintStyling` as a pure function so the
 * mapping can be unit-tested without a DOM harness — the frontend
 * test stack is vitest + happy-dom only (no @testing-library/react).
 */
function HintDisplay({
  hint,
  onDismiss,
}: {
  readonly hint: {
    readonly suggestion: string;
    readonly urgency: HintUrgency;
  };
  readonly onDismiss: () => void;
}): JSX.Element {
  const spec = hintStyleForUrgency(hint.urgency);
  const isBanner = spec.prominence === "banner";
  return (
    <aside
      style={{ ...spec.style, marginBottom: "1rem" }}
      aria-live={isBanner ? "assertive" : "polite"}
      role={isBanner ? "alert" : "status"}
    >
      {spec.label !== null && (
        <div
          style={{
            fontSize: "0.75rem",
            letterSpacing: "0.08em",
            marginBottom: "0.25rem",
          }}
        >
          {spec.label}
        </div>
      )}
      <strong>Suggestion: </strong>
      {hint.suggestion}
      {isBanner && (
        <button
          type="button"
          onClick={onDismiss}
          style={{
            marginLeft: "1rem",
            background: "none",
            border: "1px solid currentColor",
            borderRadius: "3px",
            padding: "0.1rem 0.5rem",
            color: "inherit",
            cursor: "pointer",
            fontSize: "0.75rem",
          }}
          aria-label="Dismiss hint"
        >
          Dismiss
        </button>
      )}
    </aside>
  );
}
