// frontend_web/src/lib/auth.ts
//
// Module-load-time singleton AuthProvider. Both the HostPage and the
// /auth/callback route need to operate on the SAME provider instance
// — the Cognito UserManager that fired `signinRedirect()` is the one
// that has to receive the corresponding `signinRedirectCallback()`.
// A second instance would not have the matching state in localStorage
// and would reject the callback as unknown.
//
// Side-effect at module load: wires the singleton's `getAccessToken`
// into the gateway-client transport interceptor. Every gRPC call
// thereafter carries an Authorization header automatically when the
// user is signed in (Cloud mode); Local mode's getAccessToken
// returns null so the header is omitted.

import { pickAuthProvider, type AuthProvider } from "@/providers/AuthProvider";

import { setAuthTokenGetter } from "./gateway-client";

export const auth: AuthProvider = pickAuthProvider();

setAuthTokenGetter(() => auth.getAccessToken());
