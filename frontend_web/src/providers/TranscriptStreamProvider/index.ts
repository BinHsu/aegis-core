// frontend_web/src/providers/TranscriptStreamProvider/index.ts

import { GrpcWebTranscriptStreamProvider } from "./GrpcWebTranscriptStreamProvider";
import type { TranscriptStreamProvider } from "./types";
import { WebSocketTranscriptStreamProvider } from "./WebSocketTranscriptStreamProvider";

export type {
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

export { GrpcWebTranscriptStreamProvider } from "./GrpcWebTranscriptStreamProvider";
export { WebSocketTranscriptStreamProvider } from "./WebSocketTranscriptStreamProvider";

/**
 * Pick the right transport for the current deploy mode. ADR-0007:
 * Cloud mode uses gRPC-Web; Local mode uses plain ws:// because LAN
 * IPs cannot get TLS certs.
 *
 * The mode is fixed at build time via a Vite env var; the frontend
 * never flips transports mid-session.
 */
export function pickTranscriptStreamProvider(args: {
  readonly deployMode: "cloud" | "local";
  readonly endpoint: string;
}): TranscriptStreamProvider {
  switch (args.deployMode) {
    case "cloud":
      return new GrpcWebTranscriptStreamProvider({ endpoint: args.endpoint });
    case "local":
      return new WebSocketTranscriptStreamProvider({ endpoint: args.endpoint });
  }
}
