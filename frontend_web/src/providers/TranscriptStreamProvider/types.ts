// frontend_web/src/providers/TranscriptStreamProvider/types.ts
//
// Per ADR-0007 §"Protocol Choice for Viewer -> Gateway on LAN":
// Cloud mode uses gRPC-Web (TLS, HTTP/2); Local mode uses plain
// WebSocket + Protobuf (because LAN IPs have no TLS). Both transports
// carry the same aegis.v1.ViewerEvent stream; the UI consumes them
// through this single interface.
//
// Phase 1 C3 ships the interface + transport-selection helper and
// two placeholder implementations. Real wire decoding lands in
// Phase 2 alongside the Go Gateway's fan-out.

/**
 * One live event delivered to a viewer. Mirrors aegis.v1.ViewerEvent —
 * we intentionally keep this frontend-side shape light so the UI does
 * not depend on the generated protobuf-ts surface until Phase 2 wires
 * in the real codec.
 */
export type ViewerEvent =
  | {
      readonly kind: "transcript";
      readonly sequence: number;
      readonly emittedAtMs: number;
      readonly segmentId: number;
      readonly speakerLabel: string;
      readonly text: string;
      readonly isFinal: boolean;
      readonly isQuestion: boolean;
    }
  | {
      readonly kind: "hint";
      readonly sequence: number;
      readonly emittedAtMs: number;
      readonly hintId: number;
      readonly suggestion: string;
      readonly rationale?: string;
      readonly urgency: HintUrgency;
    }
  | {
      readonly kind: "state";
      readonly sequence: number;
      readonly emittedAtMs: number;
      readonly state: MeetingState;
      readonly reason?: string;
    };

export type HintUrgency = "low" | "normal" | "high" | "urgent";

export type MeetingState =
  | "active"
  | "host-reconnecting"
  | "ended"
  | "engine-busy";

export interface JoinRequest {
  readonly sessionId: string;
  readonly viewerToken: string;
}

export interface Subscription {
  /**
   * Explicitly stop the subscription and release any underlying
   * HTTP/2 / WebSocket resource. Idempotent. The viewer UI calls
   * this on unmount and on navigation away.
   */
  unsubscribe(): void;
}

export type OnEvent = (event: ViewerEvent) => void;
export type OnError = (err: Error) => void;
export type OnClose = (reason?: string) => void;

export interface TranscriptStreamProvider {
  /**
   * Open a live subscription. Callbacks fire on the main thread
   * (no Worker for now; Phase 3+ may move framing to a worker to
   * keep the prompter 60fps under chatty transcripts).
   */
  subscribe(
    request: JoinRequest,
    callbacks: {
      readonly onEvent: OnEvent;
      readonly onError?: OnError;
      readonly onClose?: OnClose;
    },
  ): Subscription;
}
