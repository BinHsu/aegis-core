// frontend_web/src/lib/gateway-client.ts
//
// Single thin wrapper around the generated Connect-ES `Gateway` service
// client. Both the Host and Viewer pages should import the singleton
// from here rather than constructing transports themselves — that way
// the `baseUrl`, the auth-header injector, and any future cross-cutting
// concerns (request-id generation, retry policy, telemetry hooks) live
// in one auditable spot.
//
// Transport choice: `createGrpcWebTransport` from
// `@connectrpc/connect-web` speaks the grpc-web wire protocol, which
// matches the `improbable-eng/grpc-web` wrapper mounted on the Go
// Gateway's HTTP :8080 listener (see `gateway_go/cmd/gateway/main.go`
// — the IsGrpcWebRequest sniff branch). Same proto definitions across
// the wire; just a client-side library choice.
//
// Auth posture: the transport accepts an optional `getAuthToken` thunk
// that is called per-request. A nullable return means "send no
// Authorization header" — the Local-mode default. The Cloud-mode
// `CognitoAuthProvider` returns the current bearer token. The
// asymmetric closure shape avoids forcing every call site to know
// which deploy mode is active.

import { createPromiseClient, type PromiseClient } from "@connectrpc/connect";
import { createGrpcWebTransport } from "@connectrpc/connect-web";

import { Gateway } from "@/gen/proto/aegis/v1/aegis_connect.js";

/**
 * Resolved at module-load time so the rest of the app holds a stable
 * reference. Falls back to `http://localhost:8080` for the dev server
 * inner loop; production builds set `VITE_AEGIS_GATEWAY_ENDPOINT`
 * via env at `vite build` time.
 */
const GATEWAY_BASE_URL: string =
  import.meta.env["VITE_AEGIS_GATEWAY_ENDPOINT"] ?? "http://localhost:8080";

/**
 * Per-request auth-header thunk. Mutable so the AuthProvider layer can
 * register a getter once at startup; the transport re-invokes it on
 * every RPC so a token refresh during a long-lived session is picked
 * up without re-creating the client.
 */
let authTokenGetter: (() => string | null) | null = null;

/**
 * Register the function the transport will call to fetch the current
 * Authorization bearer token. Pass `null` (or omit) to revert to the
 * Local-mode "send no header" behavior.
 *
 * Idempotent: subsequent calls overwrite the prior getter. Typical
 * call site: the AuthProvider's "signed in" callback.
 */
export function setAuthTokenGetter(getter: (() => string | null) | null): void {
  authTokenGetter = getter;
}

/**
 * The Connect transport. Built once per page load. The
 * `interceptors` array is the Connect-idiomatic way to inject
 * cross-cutting per-request behavior — here we use it to layer the
 * Authorization header onto the request metadata before dispatch.
 */
const transport = createGrpcWebTransport({
  baseUrl: GATEWAY_BASE_URL,
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

/**
 * The singleton Gateway client. Re-exporting the
 * `PromiseClient<typeof Gateway>` shape so call sites can type their
 * own helper functions without re-importing the service descriptor.
 */
export const gatewayClient: PromiseClient<typeof Gateway> = createPromiseClient(
  Gateway,
  transport,
);

export { Gateway };
