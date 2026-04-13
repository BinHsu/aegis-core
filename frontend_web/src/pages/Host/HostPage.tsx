// frontend_web/src/pages/Host/HostPage.tsx
//
// Phase 1 C4. Staff host UI minimum viable surface:
//   - pick a capture mode (microphone / browser-tab / both)
//   - start / stop capture via the AudioCaptureProvider abstraction
//   - show capture state and any CaptureError surface message
//
// What's NOT here yet (Phase 2+):
//   - calling the Gateway's CreateMeeting RPC + receiving the
//     viewer JWT to display as a QR code (ADR-0001 / ADR-0007)
//   - establishing the WebRTC peer connection that ships the
//     captured stream to the Gateway
//   - rendering the live transcript / prompter
// All of those need the Gateway, which is Phase 2 work.

import { useCallback, useMemo, useState } from "react";
import {
  CaptureError,
  type CaptureMode,
  type CaptureSession,
  WebAudioCaptureProvider,
} from "@/providers/AudioCaptureProvider";

const ALL_MODES: { readonly value: CaptureMode; readonly label: string }[] = [
  { value: "microphone", label: "Physical room (microphone)" },
  { value: "browser-tab", label: "Remote meeting (capture browser tab)" },
  { value: "microphone-and-tab", label: "Both (mic + tab, mixed)" },
];

export function HostPage(): JSX.Element {
  const provider = useMemo(() => new WebAudioCaptureProvider(), []);
  const [mode, setMode] = useState<CaptureMode>("microphone");
  const [session, setSession] = useState<CaptureSession | null>(null);
  const [error, setError] = useState<string | null>(null);

  const start = useCallback(async () => {
    setError(null);
    try {
      const s = await provider.start({ mode });
      setSession(s);
    } catch (e) {
      setError(
        e instanceof CaptureError
          ? `[${e.code}] ${e.message}`
          : e instanceof Error
            ? e.message
            : String(e),
      );
    }
  }, [provider, mode]);

  const stop = useCallback(async () => {
    if (!session) return;
    await session.stop();
    setSession(null);
  }, [session]);

  const isActive = session !== null;

  return (
    <main>
      <h2>Host</h2>

      <fieldset disabled={isActive} style={{ marginBottom: "1rem" }}>
        <legend>Audio source</legend>
        {ALL_MODES.map((m) => {
          const supported = provider.isSupported(m.value);
          return (
            <label
              key={m.value}
              style={{
                display: "block",
                marginBottom: "0.25rem",
                color: supported ? "inherit" : "#aaa",
              }}
            >
              <input
                type="radio"
                name="capture-mode"
                value={m.value}
                checked={mode === m.value}
                disabled={!supported}
                onChange={() => setMode(m.value)}
              />{" "}
              {m.label}
              {!supported && (
                <span style={{ marginLeft: "0.5rem", fontSize: "0.8rem" }}>
                  (not supported in this browser)
                </span>
              )}
            </label>
          );
        })}
      </fieldset>

      {!isActive && (
        <button type="button" onClick={start}>
          Start capture
        </button>
      )}
      {isActive && (
        <button type="button" onClick={stop}>
          Stop capture
        </button>
      )}

      {error && (
        <p style={{ color: "#c0392b", marginTop: "1rem" }}>
          <strong>Capture error:</strong> {error}
        </p>
      )}

      {isActive && (
        <section style={{ marginTop: "1rem" }}>
          <p>
            Capture <strong>active</strong> in mode <code>{session?.mode}</code>
            . Stream has{" "}
            <code>{session?.stream.getAudioTracks().length ?? 0}</code> audio
            track(s).
          </p>
          <p style={{ color: "#888", fontSize: "0.85rem" }}>
            Phase 2 wires this MediaStream into a WebRTC PeerConnection and
            ships frames to the Go Gateway.
          </p>
        </section>
      )}

      <section
        style={{ marginTop: "2rem", color: "#666", fontSize: "0.85rem" }}
      >
        <p>
          Audio captured here exists only in this browser tab&apos;s memory and,
          when Phase 2 lands, is sent over WebRTC to the Aegis engine where it
          is transcribed in process RAM and discarded — see{" "}
          <a
            href="https://github.com/BinHsu/aegis-core/blob/main/docs/adr/0005-audio-ephemeral-policy.md"
            target="_blank"
            rel="noreferrer"
          >
            ADR-0005
          </a>
          .
        </p>
      </section>
    </main>
  );
}
