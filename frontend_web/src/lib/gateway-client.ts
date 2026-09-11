// frontend_web/src/lib/gateway-client.ts
//
// Single thin wrapper around the generated Connect-ES `Gateway` service
// client. Both the Host and Viewer pages reach the singleton through
// `getGatewayClient()` rather than constructing transports themselves —
// the `baseUrl`, the auth-header injector, and any future cross-cutting
// concerns (request-id generation, retry policy, telemetry hooks) live
// in one auditable spot.
//
// Transport choice: `createGrpcWebTransport` from
// `@connectrpc/connect-web` speaks the grpc-web wire protocol, which
// matches the `improbable-eng/grpc-web` wrapper mounted on the Go
// Gateway's HTTP :8080 listener (see `gateway_go/cmd/gateway/main.go`
// — the IsGrpcWebRequest sniff branch).
//
// Lifecycle (ADR-15): the transport's baseUrl comes from runtime config,
// so the client is BUILT in initGatewayClient(cfg) — called once from
// main.tsx after loadConfig() — not at module-load time. Pre-refactor
// this was a module-const resolved from import.meta.env; that baked the
// endpoint into the bundle.
//
// Auth posture: the transport calls an optional `getAuthToken` thunk
// per request. A nullable return means "send no Authorization header" —
// the Local-mode default. The Cloud-mode auth layer registers a getter
// via setAuthTokenGetter that returns the current bearer token.

// Connect-ES v2 API: `createClient` replaces v1's `createPromiseClient`
// (v1 also had a callback-style client; v2 has only the promise one, so
// the qualifier in the name became noise). `Client` likewise replaces
// `PromiseClient`.
//
// The service descriptor now comes from the protobuf-es-generated
// `aegis_pb.ts`, not from a separate `aegis_connect.ts`. Connect-ES v2
// removed `protoc-gen-connect-es` because protobuf-es v2 already emits
// a `GenService` descriptor for every service in the file. That file is
// deleted and the plugin entry is gone from buf.gen.yaml.
import { createClient, type Client } from "@connectrpc/connect";
import { createGrpcWebTransport } from "@connectrpc/connect-web";

import { Gateway } from "@/gen/proto/aegis/v1/aegis_pb.js";

import type { AppConfig } from "./config";

/**
 * Per-request auth-header thunk. Mutable so the auth layer can register
 * a getter once at startup; the transport re-invokes it on every RPC so
 * a token refresh during a long-lived session is picked up without
 * re-creating the client.
 */
let authTokenGetter: (() => string | null) | null = null;

/**
 * Register the function the transport calls to fetch the current
 * Authorization bearer token. Pass `null` to revert to the Local-mode
 * "send no header" behavior. Idempotent.
 */
export function setAuthTokenGetter(getter: (() => string | null) | null): void {
  authTokenGetter = getter;
}

let client: Client<typeof Gateway> | null = null;

/**
 * Build the singleton Gateway client from runtime config. Call once in
 * main.tsx after loadConfig(). The interceptor reads `authTokenGetter`
 * dynamically, so initAuth() may register its getter before OR after
 * this runs.
 */
export function initGatewayClient(cfg: AppConfig): void {
  const transport = createGrpcWebTransport({
    baseUrl: cfg.gatewayEndpoint,
    interceptors: [
      (next) => async (req) => {
        const token = authTokenGetter?.() ?? null;
        if (token !== null && token !== "") {
          req.header.set("Authorization", `Bearer ${token}`);
        }
        return next(req);
      },
    ],
  });
  client = createClient(Gateway, transport);
}

/**
 * The singleton Gateway client. Throws if accessed before
 * initGatewayClient() — a loud failure beats a silent unconfigured RPC.
 */
export function getGatewayClient(): Client<typeof Gateway> {
  if (client === null) {
    throw new Error(
      "gateway-client: getGatewayClient() before initGatewayClient(). " +
        "Call initGatewayClient(cfg) in main.tsx after loadConfig().",
    );
  }
  return client;
}

export { Gateway };
