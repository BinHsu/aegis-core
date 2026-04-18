// frontend_web/src/pages/Host/HostPage.tsx
//
// Phase 3. Staff host UI — the operator's full meeting lifecycle:
//
//   signed-out  ─► [Sign in]                      (Cloud mode redirect / Local no-op)
//   ready       ─► fill RAG ID + title, [Create]  (calls Gateway.CreateMeeting)
//   active      ─► QR code for viewers + audio capture + WebRTC stream
//                   to gateway + live transcript echo + [End meeting]
//   ending      ─► (calls Gateway.EndMeeting) → ready
//
// The state shape is a discriminated union (see HostState below) so
// the JSX render path branches once at the top and every branch
// guards what it can touch — no nullable session sprinkled across the
// component.
//
// The audio path:
//
//   WebAudioCaptureProvider → MediaStream (existing Phase 1 C2)
//        │
//        └► RTCPeerConnection.addTrack(audioTrack, stream)
//             │
//             └► createOffer → setLocalDescription
//                  │
//                  └► wait for ICE gathering complete (non-trickle —
//                     the gateway's pion-side Negotiator does not
//                     accept incremental candidates per ADR-0007 LAN
//                     simplification)
//                       │
//                       └► Gateway.NegotiateWebRTC(offerSdp) → answerSdp
//                            │
//                            └► setRemoteDescription(answer) — peer up.
//
// The transcript echo:
//
//   We subscribe via TranscriptStreamProvider with the same viewer
//   token we'd hand a viewer. The host watching their own session is
//   effectively viewer #1 — useful for "is the engine actually
//   transcribing?" sanity during a real meeting.

import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useState,
  type JSX,
} from "react";
import { QRCodeSVG } from "qrcode.react";

import { AudioProcessingConsent } from "@/components/AudioProcessingConsent";
import { gatewayClient } from "@/lib/gateway-client";
import { auth } from "@/lib/auth";
import {
  CaptureError,
  type CaptureMode,
  type CaptureSession,
  WebAudioCaptureProvider,
} from "@/providers/AudioCaptureProvider";
import type { AuthPrincipal } from "@/providers/AuthProvider";
import {
  pickTranscriptStreamProvider,
  type Subscription,
  type ViewerEvent,
} from "@/providers/TranscriptStreamProvider";

const ALL_MODES: { readonly value: CaptureMode; readonly label: string }[] = [
  { value: "microphone", label: "Physical room (microphone)" },
  { value: "browser-tab", label: "Remote meeting (capture browser tab)" },
  { value: "microphone-and-tab", label: "Both (mic + tab, mixed)" },
];

// Preset RAG corpora. Per ADR-0023 §"Decision B" the empty-string
// option is the DEFAULT — the chief-of-staff must opt IN to a RAG
// corpus. "No corpus" is a first-class mode, not a degraded fallback:
// plenty of meetings are better served by staff providing hints
// manually than by a mediocre retrieval hit.
//
// The remaining entries are curated presets. A live system will
// replace this with a per-tenant ListCorpora RPC once the query path
// lands (Phase 4+); until then, the list is authored here and must be
// kept in sync with what `engine seed` has actually populated in the
// target Qdrant. `aegis_taiwan` is Slice 6's seeded corpus; the
// `demo-rag-*` entries are placeholders for future demo content.
//
// The `value` is what the Gateway's CreateMeeting RPC sees in the
// `rag_id` proto field; empty string means "no RAG binding". The
// `label` is the human-friendly dropdown text.
const RAG_CORPORA: { readonly value: string; readonly label: string }[] = [
  { value: "", label: "(No corpus — staff provides hints manually)" },
  { value: "aegis_taiwan", label: "Taiwan reference (Slice 6 seeded)" },
  { value: "demo-rag-general", label: "General demo corpus" },
  { value: "demo-rag-engineering", label: "Engineering knowledge base" },
  { value: "demo-rag-sales", label: "Sales deck library" },
  { value: "demo-rag-support", label: "Customer support knowledge" },
];
const DEFAULT_RAG_ID = RAG_CORPORA[0]!.value; // "" — opt-in RAG

// Rolling transcript window shown on the host UI. Matches the
// Viewer-side `PROMPTER_WINDOW` (5) so host and viewers see the same
// line count; diverging would confuse the host about what the room
// is actually reading.
const TRANSCRIPT_TAIL = 5;

// ARCH §9.2 Speaker Labels — Privacy by Design. The UI MUST NOT
// accept free-text speaker names, because a real name converts a
// pseudonymized diarization label into identified PII and escalates
// the regulatory posture (GDPR Art. 25 "Data Protection by Design").
// This constant is the closed set of allowed relabels; the panel
// below only lets the host pick from this list. To add a new label,
// extend this constant — never wire in a free-text input.
const CURATED_SPEAKER_LABELS: readonly string[] = [
  "Host",
  "Client",
  "Colleague",
  "Guest",
  "Speaker_0",
  "Speaker_1",
  "Speaker_2",
  "Speaker_3",
  "Unknown",
] as const;

const DEPLOY_MODE = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
  | "cloud"
  | "local";
const ENDPOINT =
  import.meta.env["VITE_AEGIS_GATEWAY_ENDPOINT"] ?? "http://localhost:8080";

/**
 * The runtime data attached to an in-flight meeting. Mutable handles
 * (capture, peer, subscription) live on the dispatch-managed action
 * payloads rather than refs, so the cleanup in MEETING_ENDED can
 * reach them deterministically without dancing around closure capture.
 */
interface ActiveMeeting {
  readonly sessionId: string;
  readonly viewerToken: string;
  readonly capture: CaptureSession;
  readonly peer: RTCPeerConnection;
  readonly subscription: Subscription;
  /** Rolling tail of transcript lines (oldest first). */
  readonly transcript: readonly TranscriptLine[];
  /**
   * Client-side render gate per ADR-0024 Decision B — true if the
   * host explicitly opted into seeing the live transcript on this
   * device for this meeting. Backend keeps transcribing regardless;
   * this flag only controls what the host's browser renders.
   */
  readonly showTranscriptPanel: boolean;
  /**
   * ARCH §9.2 curated-list speaker relabels. Maps each original
   * detected label (e.g. `Speaker_0`) to a host-chosen curated
   * label (must be in `CURATED_SPEAKER_LABELS`). Empty by default;
   * entries are added when the host picks from the override panel.
   * Meeting-scoped — not persisted across meetings so a new room's
   * `Speaker_0` does not inherit the previous room's meaning.
   */
  readonly speakerOverrides: Readonly<Record<string, string>>;
}

interface TranscriptLine {
  readonly id: string; // stable key: `${segmentId}:${isFinal}`
  readonly text: string;
  readonly isFinal: boolean;
  readonly speaker: string;
}

type HostState =
  | { kind: "loading-auth" }
  | { kind: "signed-out" }
  | {
      kind: "ready";
      principal: AuthPrincipal;
      ragId: string;
      title: string;
      /** Default OFF per ADR-0024 Decision B. Carried into ActiveMeeting on start. */
      showTranscriptPanel: boolean;
    }
  | {
      kind: "creating";
      principal: AuthPrincipal;
      showTranscriptPanel: boolean;
    }
  | { kind: "active"; principal: AuthPrincipal; meeting: ActiveMeeting }
  | { kind: "ending"; principal: AuthPrincipal; meeting: ActiveMeeting }
  | { kind: "error"; principal: AuthPrincipal | null; message: string };

type Action =
  | { type: "AUTH_RESOLVED"; principal: AuthPrincipal | null }
  | { type: "FORM_FIELD_CHANGED"; field: "ragId" | "title"; value: string }
  | { type: "TRANSCRIPT_PANEL_TOGGLED"; value: boolean }
  | { type: "CREATE_REQUESTED" }
  | {
      type: "MEETING_STARTED";
      // `showTranscriptPanel` comes from `state.showTranscriptPanel`
      // (carried through `creating`), not from the action payload —
      // the reducer wires it in on transition.
      meeting: Omit<
        ActiveMeeting,
        "transcript" | "showTranscriptPanel" | "speakerOverrides"
      >;
    }
  | { type: "TRANSCRIPT_LINE"; line: TranscriptLine }
  | {
      type: "SPEAKER_LABEL_ASSIGNED";
      originalLabel: string;
      /** Empty string clears the override back to the original label. */
      curatedLabel: string;
    }
  | { type: "END_REQUESTED" }
  | { type: "MEETING_ENDED" }
  | { type: "ERROR_RAISED"; message: string }
  | { type: "ERROR_CLEARED" };

function reducer(state: HostState, action: Action): HostState {
  switch (action.type) {
    case "AUTH_RESOLVED": {
      if (action.principal === null) return { kind: "signed-out" };
      return {
        kind: "ready",
        principal: action.principal,
        ragId: DEFAULT_RAG_ID,
        title: "",
        showTranscriptPanel: false, // ADR-0024 Decision B: default OFF
      };
    }
    case "FORM_FIELD_CHANGED": {
      if (state.kind !== "ready") return state;
      return { ...state, [action.field]: action.value };
    }
    case "TRANSCRIPT_PANEL_TOGGLED": {
      if (state.kind !== "ready") return state;
      return { ...state, showTranscriptPanel: action.value };
    }
    case "CREATE_REQUESTED": {
      if (state.kind !== "ready") return state;
      return {
        kind: "creating",
        principal: state.principal,
        showTranscriptPanel: state.showTranscriptPanel,
      };
    }
    case "MEETING_STARTED": {
      if (state.kind !== "creating") return state;
      return {
        kind: "active",
        principal: state.principal,
        meeting: {
          ...action.meeting,
          transcript: [],
          showTranscriptPanel: state.showTranscriptPanel,
          speakerOverrides: {},
        },
      };
    }
    case "TRANSCRIPT_LINE": {
      if (state.kind !== "active") return state;
      // Replace-or-append: an interim segment with the same id is
      // overwritten by its final form. The id contains isFinal so
      // a finalized version naturally has a different key — handle
      // that by pruning prior interims of the same numeric segmentId.
      const numericId = action.line.id.split(":")[0];
      const filtered = state.meeting.transcript.filter(
        (l) => l.id.split(":")[0] !== numericId,
      );
      const next = [...filtered, action.line];
      const trimmed =
        next.length > TRANSCRIPT_TAIL
          ? next.slice(next.length - TRANSCRIPT_TAIL)
          : next;
      return {
        ...state,
        meeting: { ...state.meeting, transcript: trimmed },
      };
    }
    case "SPEAKER_LABEL_ASSIGNED": {
      if (state.kind !== "active") return state;
      // Reject labels outside the curated list — defense in depth
      // against a caller constructing the action with a freeform
      // value. Empty string is allowed as the "clear override" signal.
      if (
        action.curatedLabel !== "" &&
        !CURATED_SPEAKER_LABELS.includes(action.curatedLabel)
      ) {
        return state;
      }
      const next = { ...state.meeting.speakerOverrides };
      if (action.curatedLabel === "") {
        delete next[action.originalLabel];
      } else {
        next[action.originalLabel] = action.curatedLabel;
      }
      return {
        ...state,
        meeting: { ...state.meeting, speakerOverrides: next },
      };
    }
    case "END_REQUESTED": {
      if (state.kind !== "active") return state;
      return {
        kind: "ending",
        principal: state.principal,
        meeting: state.meeting,
      };
    }
    case "MEETING_ENDED": {
      const principal =
        state.kind === "active" || state.kind === "ending"
          ? state.principal
          : state.kind === "ready" || state.kind === "creating"
            ? state.principal
            : null;
      if (principal === null) return { kind: "signed-out" };
      return {
        kind: "ready",
        principal,
        ragId: "",
        title: "",
        showTranscriptPanel: false,
      };
    }
    case "ERROR_RAISED": {
      // Carry forward the principal if we have one so the user lands
      // back on a sensible "ready" path, not signed-out.
      const principal = "principal" in state ? state.principal : null;
      return { kind: "error", principal, message: action.message };
    }
    case "ERROR_CLEARED": {
      if (state.kind !== "error") return state;
      if (state.principal === null) return { kind: "signed-out" };
      return {
        kind: "ready",
        principal: state.principal,
        ragId: "",
        title: "",
        showTranscriptPanel: false,
      };
    }
  }
}

export function HostPage(): JSX.Element {
  const [state, dispatch] = useReducer(reducer, { kind: "loading-auth" });

  // LAN IP for building QR-code URLs that viewers on other devices
  // (phones) can actually reach. Browser JS can't query OS interfaces
  // itself — the Go gateway exposes `/lan-ip` for this. `null` while
  // fetching; empty string if the fetch failed; LAN IP string on success.
  const [lanIP, setLanIP] = useState<string | null>(null);

  // Single AudioCaptureProvider for the page. Stateless across
  // start/stop cycles; safe to memoize.
  const captureProvider = useMemo(() => new WebAudioCaptureProvider(), []);

  // ──────────────────────────────────────────────────────────────────
  // Auth subscription. Local mode fires once with the synthetic
  // principal; Cloud mode fires on sign-in completion.
  // ──────────────────────────────────────────────────────────────────
  useEffect(() => {
    const unsubscribe = auth.onChange((principal) => {
      dispatch({ type: "AUTH_RESOLVED", principal });
    });
    return unsubscribe;
  }, []);

  // ──────────────────────────────────────────────────────────────────
  // Fetch LAN IP once at mount. Used to build a QR URL that's
  // reachable from phones on the same LAN. Falls back to empty string
  // on any failure — the QR section degrades to "access the host via
  // your LAN IP to share a working QR" hint.
  // ──────────────────────────────────────────────────────────────────
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const res = await fetch(`${ENDPOINT}/lan-ip`);
        if (!res.ok) throw new Error(`/lan-ip returned ${res.status}`);
        const json = (await res.json()) as { best?: string };
        if (!cancelled) {
          setLanIP(json.best ?? "");
        }
      } catch (err) {
        console.warn("[host] failed to fetch /lan-ip:", err);
        if (!cancelled) setLanIP("");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  // ──────────────────────────────────────────────────────────────────
  // CreateMeeting + WebRTC negotiation + transcript subscription.
  // Wraps everything in one async function so partial success cleans
  // up the partials before propagating the error.
  // ──────────────────────────────────────────────────────────────────
  const startMeeting = useCallback(
    async (ragId: string, title: string, mode: CaptureMode) => {
      dispatch({ type: "CREATE_REQUESTED" });

      let capture: CaptureSession | null = null;
      let peer: RTCPeerConnection | null = null;
      let subscription: Subscription | null = null;
      try {
        // 1. Audio first — if mic permission is denied, fail fast
        //    before allocating a Gateway session that would just
        //    sit idle.
        capture = await captureProvider.start({ mode });
        const audioTrack = capture.stream.getAudioTracks()[0];
        if (!audioTrack) {
          throw new Error("captured stream has no audio track");
        }

        // 2. CreateMeeting RPC.
        const createResp = await gatewayClient.createMeeting({
          ragId,
          title,
          languageHints: [],
          allowedViewerAccountIds: [],
        });

        // 3. Build the RTCPeerConnection. No explicit ICE servers —
        //    matches the gateway's pion-side Negotiator (ADR-0007 LAN
        //    assumption). Add the audio transceiver as send-only.
        peer = new RTCPeerConnection();
        peer.addTransceiver(audioTrack, {
          direction: "sendonly",
          streams: [capture.stream],
        });

        // 4. Non-trickle SDP exchange: create offer, gather all ICE
        //    candidates inline, send the complete SDP to NegotiateWebRTC.
        const offer = await peer.createOffer();
        await peer.setLocalDescription(offer);
        await waitForIceGatheringComplete(peer);
        if (!peer.localDescription) {
          throw new Error("peer.localDescription unexpectedly null");
        }

        const negResp = await gatewayClient.negotiateWebRTC({
          sessionId: createResp.sessionId,
          offerSdp: peer.localDescription.sdp,
        });
        await peer.setRemoteDescription({
          type: "answer",
          sdp: negResp.answerSdp,
        });

        // 5. Subscribe to the transcript stream as if we were a
        //    viewer. The host watching their own session catches
        //    "engine isn't transcribing" instantly.
        const streamProvider = pickTranscriptStreamProvider({
          deployMode: DEPLOY_MODE,
          endpoint: ENDPOINT,
        });
        subscription = streamProvider.subscribe(
          {
            sessionId: createResp.sessionId,
            viewerToken: createResp.viewerJoinToken,
          },
          {
            onEvent: (event) => {
              const line = transcriptLineFromEvent(event);
              if (line !== null) {
                dispatch({ type: "TRANSCRIPT_LINE", line });
              }
            },
            onError: (err) => {
              // Non-fatal at this layer — the meeting can keep
              // running even if the host's own echo subscription
              // dies. Surface as an inline error log; viewers are
              // unaffected.
              console.warn("[host] transcript stream error:", err);
            },
          },
        );

        dispatch({
          type: "MEETING_STARTED",
          meeting: {
            sessionId: createResp.sessionId,
            viewerToken: createResp.viewerJoinToken,
            capture,
            peer,
            subscription,
          },
        });
      } catch (err) {
        // Roll back partial state. Each handle is null until its
        // corresponding step succeeded, so this is safe.
        if (subscription) subscription.unsubscribe();
        if (peer) peer.close();
        if (capture) await capture.stop().catch(() => undefined);
        const message =
          err instanceof CaptureError
            ? `Capture: [${err.code}] ${err.message}`
            : err instanceof Error
              ? err.message
              : String(err);
        dispatch({ type: "ERROR_RAISED", message });
      }
    },
    [captureProvider],
  );

  const endMeeting = useCallback(async (active: ActiveMeeting) => {
    dispatch({ type: "END_REQUESTED" });
    // Best-effort cleanup, in shutdown order: viewer-facing
    // subscription → audio capture → peer → server-side EndMeeting.
    // Each step is independent; we don't want one failure to leak
    // the others.
    try {
      active.subscription.unsubscribe();
    } catch (err) {
      console.warn("[host] subscription.unsubscribe:", err);
    }
    try {
      active.peer.close();
    } catch (err) {
      console.warn("[host] peer.close:", err);
    }
    try {
      await active.capture.stop();
    } catch (err) {
      console.warn("[host] capture.stop:", err);
    }
    try {
      await gatewayClient.endMeeting({ sessionId: active.sessionId });
    } catch (err) {
      console.warn("[host] EndMeeting RPC:", err);
    }
    dispatch({ type: "MEETING_ENDED" });
  }, []);

  // ──────────────────────────────────────────────────────────────────
  // Render
  // ──────────────────────────────────────────────────────────────────
  if (state.kind === "loading-auth") {
    return (
      <main>
        <h2>Host</h2>
        <p style={{ color: "#666" }}>Resolving identity…</p>
      </main>
    );
  }

  if (state.kind === "signed-out") {
    return (
      <main>
        <h2>Host</h2>
        <p>You need to sign in to start a meeting.</p>
        <button
          type="button"
          onClick={() => {
            void auth.signIn();
          }}
        >
          Sign in {DEPLOY_MODE === "cloud" ? "with Cognito" : "(local)"}
        </button>
      </main>
    );
  }

  if (state.kind === "error") {
    return (
      <main>
        <h2>Host</h2>
        <p style={{ color: "#c0392b" }}>
          <strong>Error:</strong> {state.message}
        </p>
        <button
          type="button"
          onClick={() => dispatch({ type: "ERROR_CLEARED" })}
        >
          Dismiss
        </button>
      </main>
    );
  }

  if (state.kind === "ready" || state.kind === "creating") {
    const isCreating = state.kind === "creating";
    const ragId = state.kind === "ready" ? state.ragId : DEFAULT_RAG_ID;
    const title = state.kind === "ready" ? state.title : "";
    const showTranscriptPanel = state.showTranscriptPanel;
    return (
      <main>
        <Header principal={state.principal} />
        <AudioProcessingConsent userId={state.principal.userId} />
        <h3>Start a new meeting</h3>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const fd = new FormData(e.currentTarget);
            const mode = (fd.get("capture-mode") ??
              "microphone") as CaptureMode;
            void startMeeting(ragId, title.trim(), mode);
          }}
        >
          <label style={{ display: "block", marginBottom: "0.5rem" }}>
            RAG corpus:&nbsp;
            <select
              value={ragId}
              disabled={isCreating}
              onChange={(e) =>
                dispatch({
                  type: "FORM_FIELD_CHANGED",
                  field: "ragId",
                  value: e.target.value,
                })
              }
            >
              {RAG_CORPORA.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
          <label style={{ display: "block", marginBottom: "0.5rem" }}>
            Meeting title:&nbsp;
            <input
              type="text"
              value={title}
              maxLength={200}
              disabled={isCreating}
              onChange={(e) =>
                dispatch({
                  type: "FORM_FIELD_CHANGED",
                  field: "title",
                  value: e.target.value,
                })
              }
            />
          </label>

          {/*
            Transcript panel opt-in (ADR-0024 Decision B). Default
            OFF. Turning ON shows the GDPR notice so the host
            acknowledges the on-screen-data-exposure risk before the
            transcript renders live.
          */}
          <fieldset disabled={isCreating} style={{ marginBottom: "1rem" }}>
            <legend>Live transcript panel</legend>
            <label style={{ display: "block", marginBottom: "0.25rem" }}>
              <input
                type="checkbox"
                checked={showTranscriptPanel}
                onChange={(e) =>
                  dispatch({
                    type: "TRANSCRIPT_PANEL_TOGGLED",
                    value: e.target.checked,
                  })
                }
              />{" "}
              Show live transcript on this screen
            </label>
            {showTranscriptPanel && (
              <p
                style={{
                  margin: "0.3rem 0 0 1.6rem",
                  fontSize: "0.82rem",
                  color: "#555",
                  maxWidth: "44rem",
                }}
              >
                Turning on the live transcript shows meeting content on your
                screen. Aegis processes this data under GDPR Art. 6(1)(f)
                (legitimate interests — operating the service you requested)
                and, where participants' messages include special-category data,
                Art. 9(2)(a) (your explicit consent to see it). You are
                responsible for the physical security of your screen
                (bystanders, recording devices) while the panel is visible.
              </p>
            )}
          </fieldset>

          <fieldset disabled={isCreating} style={{ marginBottom: "1rem" }}>
            <legend>Audio source</legend>
            {ALL_MODES.map((m) => {
              const supported = captureProvider.isSupported(m.value);
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
                    defaultChecked={m.value === "microphone"}
                    disabled={!supported}
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

          <button type="submit" disabled={isCreating}>
            {isCreating ? "Creating…" : "New meeting"}
          </button>
        </form>
      </main>
    );
  }

  // active or ending
  const meeting = state.meeting;
  const isEnding = state.kind === "ending";

  // Build the viewer URL. Prefer the LAN IP from /lan-ip so a phone
  // scanning the QR can actually reach the dev server (which binds
  // on 0.0.0.0 per vite.config.ts `server.host: true`). Fall back to
  // `window.location.origin` while the fetch is in flight / failed —
  // that still works for same-machine verification, just not for
  // off-device viewers.
  const isLoopback =
    window.location.hostname === "localhost" ||
    window.location.hostname === "127.0.0.1";
  const viewerOrigin =
    lanIP !== null && lanIP !== "" && isLoopback
      ? `${window.location.protocol}//${lanIP}:${
          window.location.port || "5173"
        }`
      : window.location.origin;
  const viewerUrl = `${viewerOrigin}/view/${
    meeting.sessionId
  }?token=${encodeURIComponent(meeting.viewerToken)}`;
  const lanQRReady = !isLoopback || (lanIP !== null && lanIP !== "");

  return (
    <main>
      <Header principal={state.principal} />
      <AudioProcessingConsent userId={state.principal.userId} />
      <h3>Meeting active</h3>

      <section
        style={{
          display: "grid",
          gridTemplateColumns: "auto 1fr",
          gap: "1.5rem",
          alignItems: "start",
        }}
      >
        <div>
          <p style={{ marginTop: 0 }}>
            <strong>Viewers scan to join:</strong>
          </p>
          <QRCodeSVG value={viewerUrl} size={180} includeMargin />
          <p style={{ fontSize: "0.75rem", color: "#888", maxWidth: 220 }}>
            Or share this link:
            <br />
            <code style={{ wordBreak: "break-all" }}>{viewerUrl}</code>
          </p>
          {!lanQRReady && (
            <p
              style={{
                fontSize: "0.75rem",
                color: "#c0392b",
                maxWidth: 220,
                marginTop: "0.5rem",
              }}
            >
              ⚠️ QR encodes <code>localhost</code> because the gateway
              didn&apos;t report a LAN IP (interfaces down?). Phones scanning it
              will fail. Open this page via your LAN IP (e.g.{" "}
              <code>http://192.168.x.y:5173/host</code>) or restart the gateway
              with a working network.
            </p>
          )}
        </div>

        <div>
          <p>
            Session: <code>{meeting.sessionId}</code>
            <br />
            Audio track: <code>{meeting.capture.mode}</code> · WebRTC peer
            state: <code>{meeting.peer.connectionState}</code>
          </p>
          <h4 style={{ marginBottom: "0.25rem" }}>Live transcript (echo)</h4>
          {/*
            ADR-0024 Decision B render gate. When the host did not opt
            in to the panel, we show a placeholder instead of the
            transcript lines. The backend keeps transcribing either
            way — this is purely a client-side visibility choice.
            ADR-0024 Decision D also forbids disabling text selection
            on the transcript (screenshots bypass it, breaks screen
            readers); the compliance script enforces that rule.
          */}
          {!meeting.showTranscriptPanel ? (
            <p style={{ color: "#888", fontStyle: "italic" }}>
              Transcript display disabled for this meeting. Toggle on in the New
              Meeting form to show transcript lines on this screen (default OFF
              per consent posture).
            </p>
          ) : meeting.transcript.length === 0 ? (
            <p style={{ color: "#888", fontStyle: "italic" }}>
              Waiting for the first segment…
            </p>
          ) : (
            <>
              <SpeakerLabelPanel
                transcript={meeting.transcript}
                overrides={meeting.speakerOverrides}
                onAssign={(originalLabel, curatedLabel) =>
                  dispatch({
                    type: "SPEAKER_LABEL_ASSIGNED",
                    originalLabel,
                    curatedLabel,
                  })
                }
              />
              <ul style={{ paddingLeft: "1rem" }}>
                {meeting.transcript.map((line) => {
                  const displayLabel =
                    meeting.speakerOverrides[line.speaker] ?? line.speaker;
                  return (
                    <li
                      key={line.id}
                      style={{
                        color: line.isFinal ? "#000" : "#888",
                        fontStyle: line.isFinal ? "normal" : "italic",
                      }}
                    >
                      <strong>{displayLabel}:</strong> {line.text}
                    </li>
                  );
                })}
              </ul>
            </>
          )}

          <button
            type="button"
            onClick={() => void endMeeting(meeting)}
            disabled={isEnding}
            style={{ marginTop: "1rem" }}
          >
            {isEnding ? "Ending…" : "End meeting"}
          </button>
        </div>
      </section>
    </main>
  );
}

/**
 * ARCH §9.2 Speaker Labels panel. Shows every distinct original
 * diarization label that has appeared in the transcript tail, plus
 * a curated-list <select> to override each. No free-text field —
 * the closed set lives in `CURATED_SPEAKER_LABELS`, so the host
 * cannot type a real name that would convert the label into
 * identified PII (GDPR Art. 25).
 *
 * The "(original)" option clears an override back to the detected
 * label without forcing a refresh, so a misclick is recoverable.
 */
function SpeakerLabelPanel({
  transcript,
  overrides,
  onAssign,
}: {
  readonly transcript: readonly TranscriptLine[];
  readonly overrides: Readonly<Record<string, string>>;
  readonly onAssign: (originalLabel: string, curatedLabel: string) => void;
}): JSX.Element | null {
  // Distinct detected labels in the order they first appeared in the
  // rolling tail. Using a simple loop keeps the order stable; Set's
  // iteration order is insertion order in modern V8 but the explicit
  // loop makes the invariant obvious.
  const distinct: string[] = [];
  for (const line of transcript) {
    if (!distinct.includes(line.speaker)) distinct.push(line.speaker);
  }
  if (distinct.length === 0) return null;
  return (
    <fieldset
      style={{
        border: "1px solid #eee",
        padding: "0.5rem 0.75rem",
        margin: "0.25rem 0 0.75rem",
        fontSize: "0.85rem",
      }}
    >
      <legend style={{ padding: "0 0.4rem", color: "#555" }}>
        Speaker labels (curated list only — no free-text per ARCH §9.2)
      </legend>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "0.75rem" }}>
        {distinct.map((original) => {
          const current = overrides[original] ?? "";
          return (
            <label
              key={original}
              style={{ display: "inline-flex", alignItems: "center" }}
            >
              <code style={{ marginRight: "0.3rem" }}>{original}</code>→{" "}
              <select
                value={current}
                onChange={(e) => onAssign(original, e.target.value)}
                style={{ marginLeft: "0.3rem" }}
              >
                <option value="">(original)</option>
                {CURATED_SPEAKER_LABELS.map((label) => (
                  <option key={label} value={label}>
                    {label}
                  </option>
                ))}
              </select>
            </label>
          );
        })}
      </div>
    </fieldset>
  );
}

function Header({ principal }: { principal: AuthPrincipal }): JSX.Element {
  return (
    <header style={{ marginBottom: "1rem", color: "#666", fontSize: "0.9rem" }}>
      Signed in as <code>{principal.userId}</code>
      {principal.tenantId !== "" && (
        <>
          {" · tenant "}
          <code>{principal.tenantId}</code>
        </>
      )}
      {" · "}
      <button
        type="button"
        style={{
          background: "none",
          border: "none",
          color: "#3498db",
          cursor: "pointer",
          padding: 0,
          font: "inherit",
          textDecoration: "underline",
        }}
        onClick={() => {
          void auth.signOut();
        }}
      >
        sign out
      </button>
    </header>
  );
}

/**
 * Resolve once the RTCPeerConnection has finished gathering ICE
 * candidates — required because the gateway's pion Negotiator does
 * not accept trickle ICE per ADR-0007 LAN assumption. The peer has
 * a `gatherstatechange` event but it can fire before we attach the
 * listener; check the synchronous state first.
 */
function waitForIceGatheringComplete(peer: RTCPeerConnection): Promise<void> {
  if (peer.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const onChange = () => {
      if (peer.iceGatheringState === "complete") {
        peer.removeEventListener("icegatheringstatechange", onChange);
        resolve();
      }
    };
    peer.addEventListener("icegatheringstatechange", onChange);
  });
}

/**
 * Map a ViewerEvent (from the TranscriptStreamProvider abstraction)
 * into the host-page TranscriptLine shape, or null for events the
 * host UI doesn't render (state changes, hints).
 */
function transcriptLineFromEvent(event: ViewerEvent): TranscriptLine | null {
  if (event.kind !== "transcript") return null;
  return {
    id: `${event.segmentId}:${event.isFinal ? "final" : "interim"}`,
    text: event.text,
    isFinal: event.isFinal,
    speaker: event.speakerLabel ?? "Speaker",
  };
}
