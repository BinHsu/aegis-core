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

import { createPromiseClient, type PromiseClient } from "@connectrpc/connect";
import { createGrpcWebTransport } from "@connectrpc/connect-web";

import { Gateway } from "@/gen/proto/aegis/v1/aegis_connect.js";

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

let client: PromiseClient<typeof Gateway> | null = null;

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
  client = createPromiseClient(Gateway, transport);
}

/**
 * The singleton Gateway client. Throws if accessed before
 * initGatewayClient() — a loud failure beats a silent unconfigured RPC.
 */
export function getGatewayClient(): PromiseClient<typeof Gateway> {
  if (client === null) {
    throw new Error(
      "gateway-client: getGatewayClient() before initGatewayClient(). " +
        "Call initGatewayClient(cfg) in main.tsx after loadConfig().",
    );
  }
  return client;
}

export { Gateway };
