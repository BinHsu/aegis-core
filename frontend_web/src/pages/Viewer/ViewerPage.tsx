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
  type MeetingState,
  pickTranscriptStreamProvider,
  type ViewerEvent,
} from "@/providers/TranscriptStreamProvider";

const PROMPTER_WINDOW = 5;

// Deploy mode + endpoint are normally injected at build time via Vite
// env vars. The defaults here are the Local mode fallback so the
// dev server experience works out of the box.
const DEPLOY_MODE = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
  | "cloud"
  | "local";
const ENDPOINT =
  import.meta.env["VITE_AEGIS_GATEWAY_ENDPOINT"] ?? "http://localhost:8080";

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
  const [hint, setHint] = useState<string | null>(null);
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
          setHint(event.suggestion);
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

      {hint && (
        <aside
          style={{
            padding: "0.75rem 1rem",
            background: "#e8f4fd",
            border: "1px solid #bce0f5",
            borderRadius: "4px",
            marginBottom: "1rem",
          }}
        >
          <strong>Suggestion: </strong>
          {hint}
        </aside>
      )}

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
