// frontend_web/src/providers/TranscriptStreamProvider/GrpcWebTranscriptStreamProvider.ts
//
// Cloud-mode transport per ADR-0007. The Gateway's aegis.v1.Gateway
// service exposes JoinAsViewer as a server-streaming RPC; in Cloud
// this is reachable via gRPC-Web (HTTP/2 + TLS). The browser client
// uses @bufbuild/connect-web (or equivalent) for framing.
//
// Phase 1 C3 ships a Placeholder-like implementation that returns
// an idle Subscription; Phase 2 wires this to the real transport
// once the Gateway's JoinAsViewer handler exists.

import type {
  JoinRequest,
  OnClose,
  OnError,
  OnEvent,
  Subscription,
  TranscriptStreamProvider,
} from "./types";

export interface GrpcWebConfig {
  /** Base URL of the Gateway's gRPC-Web endpoint, e.g. `https://aegis.example.com`. */
  readonly endpoint: string;
}

export class GrpcWebTranscriptStreamProvider
  implements TranscriptStreamProvider
{
  private readonly config: GrpcWebConfig;

  constructor(config: GrpcWebConfig) {
    this.config = config;
  }

  subscribe(
    _request: JoinRequest,
    callbacks: {
      readonly onEvent: OnEvent;
      readonly onError?: OnError;
      readonly onClose?: OnClose;
    },
  ): Subscription {
    // Phase 2 TODO: use @bufbuild/connect-web (or grpc-web) to call
    // aegis.v1.Gateway.JoinAsViewer(session_id, viewer_token). Each
    // server-streamed ViewerEvent message is decoded via the
    // generated protobuf-ts bindings and forwarded through onEvent.
    //
    // The decoding / transport split lives here; the callback
    // contract (ViewerEvent discriminated union) is stable and does
    // not leak transport types to pages.
    //
    // Session 4d's engine already returns TranscriptSegment on the
    // internal StreamTranscribe path; the Gateway's JoinAsViewer
    // fan-out just forwards them to viewers.

    let cancelled = false;

    // Emit a synthetic connecting/pending state so the Viewer UI has
    // something to render before the real transport is wired up.
    queueMicrotask(() => {
      if (!cancelled) {
        callbacks.onEvent({
          kind: "state",
          sequence: 0,
          emittedAtMs: Date.now(),
          state: "host-reconnecting",
          reason: `stub transport (Cloud endpoint ${this.config.endpoint}); Phase 2 wires the real gRPC-Web channel`,
        });
      }
    });

    return {
      unsubscribe: () => {
        if (cancelled) return;
        cancelled = true;
        callbacks.onClose?.("unsubscribed");
      },
    };
  }
}
