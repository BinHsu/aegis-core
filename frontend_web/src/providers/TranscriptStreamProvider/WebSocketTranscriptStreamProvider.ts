// frontend_web/src/providers/TranscriptStreamProvider/WebSocketTranscriptStreamProvider.ts
//
// Local-mode transport per ADR-0007 §"Protocol Choice for Viewer ->
// Gateway on LAN". Browsers require a secure context for many modern
// APIs; `localhost` is exempt but LAN IPs are not, which makes
// gRPC-Web over HTTP/1.1 unreliable on the boss's phone visiting
// http://192.168.1.42:8080/view/... Plain `ws://` WebSocket is
// allowed from insecure contexts on all major browsers, so Local
// mode uses WebSocket + Protobuf binary framing.
//
// Wire format: each ws.send is a single binary frame carrying one
// `aegis.v1.ViewerEvent` marshalled via proto3. The Go Gateway
// side (`gateway_go/internal/ws/ws.go` `sendEvent`) does the mirror
// encode. Earlier this file was a Phase-1 stub that only handled
// string frames and dropped binary — the bug surfaced as "transcript
// segments emit on engine, broadcast delivers to WS subscriber, but
// host UI shows nothing". See docs/incidents.md for context.

import {
  HintUrgency as ProtoHintUrgency,
  MeetingState as ProtoMeetingState,
  ViewerEvent as ProtoViewerEvent,
} from "@/gen/proto/aegis/v1/aegis_pb";

import type {
  HintUrgency,
  JoinRequest,
  MeetingState,
  OnClose,
  OnError,
  OnEvent,
  Subscription,
  TranscriptStreamProvider,
  ViewerEvent,
} from "./types";

export interface WebSocketConfig {
  /**
   * LAN-mode endpoint URL. The host UI displays a QR code encoding
   * this URL (plus /view/<session_id>?token=<jwt>) at meeting start;
   * the viewer scans and the browser opens the page. A separate
   * `ws://` connection to the same host:port carries the stream.
   */
  readonly endpoint: string;
}

function buildWebSocketUrl(cfg: WebSocketConfig, req: JoinRequest): string {
  const base = cfg.endpoint.replace(/^http/, "ws").replace(/\/$/, "");
  const sid = encodeURIComponent(req.sessionId);
  const token = encodeURIComponent(req.viewerToken);
  return `${base}/ws/viewer?session_id=${sid}&token=${token}`;
}

// bigintToNumberSafely — protoInt64 fields (sequence, segment_id, etc.)
// arrive as bigint. The UI never reasons about values above 2^53, so the
// narrow cast is safe for expected cardinality; if a value somehow
// overflows we clamp to Number.MAX_SAFE_INTEGER rather than returning NaN
// or throwing — a degraded but still-rendered segment is the correct
// outcome for a local-mode viewer.
function bigintToNumber(v: bigint): number {
  if (v > BigInt(Number.MAX_SAFE_INTEGER)) return Number.MAX_SAFE_INTEGER;
  return Number(v);
}

function timestampToMs(
  ts: { seconds: bigint; nanos: number } | undefined,
): number {
  if (!ts) return Date.now();
  return bigintToNumber(ts.seconds) * 1000 + Math.floor(ts.nanos / 1e6);
}

function mapMeetingState(s: ProtoMeetingState): MeetingState {
  switch (s) {
    case ProtoMeetingState.ACTIVE:
      return "active";
    case ProtoMeetingState.HOST_RECONNECTING:
      return "host-reconnecting";
    case ProtoMeetingState.ENDED:
      return "ended";
    case ProtoMeetingState.ENGINE_BUSY:
      return "engine-busy";
    default:
      return "active";
  }
}

function mapHintUrgency(u: ProtoHintUrgency): HintUrgency {
  switch (u) {
    case ProtoHintUrgency.LOW:
      return "low";
    case ProtoHintUrgency.HIGH:
      return "high";
    case ProtoHintUrgency.URGENT:
      return "urgent";
    default:
      return "normal";
  }
}

// decodeBinaryFrame — parse a single gateway-emitted binary WebSocket
// frame into the local ViewerEvent shape the UI consumes. Returns null
// if the proto payload oneof is unset (indicates a malformed frame
// or a future variant we don't know how to render yet).
function decodeBinaryFrame(buf: ArrayBuffer): ViewerEvent | null {
  const proto = ProtoViewerEvent.fromBinary(new Uint8Array(buf));
  const emittedAtMs = timestampToMs(proto.emittedAt);
  const sequence = bigintToNumber(proto.sequence);

  switch (proto.payload.case) {
    case "transcript": {
      const t = proto.payload.value;
      return {
        kind: "transcript",
        sequence,
        emittedAtMs,
        segmentId: bigintToNumber(t.segmentId),
        speakerLabel: t.speakerLabel,
        text: t.text,
        isFinal: t.isFinal,
        isQuestion: t.isQuestion,
      };
    }
    case "hint": {
      const h = proto.payload.value;
      // Conditional spread for optional fields — types.ts declares
      // `rationale?: string` with `exactOptionalPropertyTypes: true`,
      // which forbids assigning `undefined` explicitly.
      return {
        kind: "hint",
        sequence,
        emittedAtMs,
        hintId: bigintToNumber(h.hintId),
        suggestion: h.suggestion,
        urgency: mapHintUrgency(h.urgency),
        ...(h.rationale ? { rationale: h.rationale } : {}),
      };
    }
    case "stateChange": {
      const s = proto.payload.value;
      return {
        kind: "state",
        sequence,
        emittedAtMs,
        state: mapMeetingState(s.state),
        ...(s.reason ? { reason: s.reason } : {}),
      };
    }
    default:
      return null;
  }
}

export class WebSocketTranscriptStreamProvider
  implements TranscriptStreamProvider
{
  private readonly config: WebSocketConfig;

  constructor(config: WebSocketConfig) {
    this.config = config;
  }

  subscribe(
    request: JoinRequest,
    callbacks: {
      readonly onEvent: OnEvent;
      readonly onError?: OnError;
      readonly onClose?: OnClose;
    },
  ): Subscription {
    let closed = false;
    const url = buildWebSocketUrl(this.config, request);

    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";

    ws.onmessage = (ev: MessageEvent): void => {
      if (ev.data instanceof ArrayBuffer) {
        let event: ViewerEvent | null = null;
        try {
          event = decodeBinaryFrame(ev.data);
        } catch (err) {
          callbacks.onError?.(
            new Error(
              `failed to decode ViewerEvent binary frame: ${
                err instanceof Error ? err.message : String(err)
              }`,
            ),
          );
          return;
        }
        if (event !== null) {
          callbacks.onEvent(event);
        }
        return;
      }
      if (typeof ev.data === "string") {
        // Gateway never emits text frames; if one arrives it is either
        // a test double or a protocol bug. Treat as non-fatal — log via
        // onError so the caller (HostPage / ViewerPage) can surface it
        // in the console without tearing the subscription down.
        callbacks.onError?.(
          new Error(
            `unexpected text frame on Local-mode WS (expected binary ViewerEvent): ${ev.data.slice(
              0,
              80,
            )}`,
          ),
        );
      }
    };

    ws.onerror = (): void => {
      callbacks.onError?.(new Error("WebSocket error (Local mode transport)"));
    };

    ws.onclose = (event: CloseEvent): void => {
      if (closed) return;
      closed = true;
      callbacks.onClose?.(
        `ws closed code=${event.code} reason=${event.reason || "(none)"}`,
      );
    };

    return {
      unsubscribe: () => {
        if (closed) return;
        closed = true;
        try {
          ws.close(1000, "unsubscribed");
        } catch {
          /* ignore */
        }
      },
    };
  }
}
