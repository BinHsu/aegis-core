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
// Phase 1 C3 ships the transport wrapper; Phase 2 adds real
// protobuf-ts decoding of ViewerEvent frames once the Gateway
// side exists.

import type {
  JoinRequest,
  OnClose,
  OnError,
  OnEvent,
  Subscription,
  TranscriptStreamProvider,
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
      // Phase 2: decode ev.data as a binary-wire aegis.v1.ViewerEvent
      // via protobuf-ts and forward to onEvent. For now, emit a
      // synthetic event proving the WS connected.
      if (typeof ev.data === "string") {
        // Some test servers echo plain text; accept + log.
        callbacks.onEvent({
          kind: "state",
          sequence: 0,
          emittedAtMs: Date.now(),
          state: "active",
          reason: `received text frame (${ev.data.length} chars); Phase 2 decodes real protobuf`,
        });
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
