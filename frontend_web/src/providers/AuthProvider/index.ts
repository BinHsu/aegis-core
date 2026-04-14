// frontend_web/src/providers/AuthProvider/index.ts
//
// Public surface of the AuthProvider port. Re-exports the types and
// concrete implementations, plus a deploy-mode-driven factory that
// the App-level wiring uses.
//
// Design intent: app code imports `pickAuthProvider()` (or the
// concrete classes for tests); never the implementations directly.
// That keeps mode-switching a one-line change in the env config.

import { CognitoAuthProvider } from "./CognitoAuthProvider";
import { LocalAuthProvider } from "./LocalAuthProvider";
import type { AuthProvider } from "./types";

export type {
  AuthChangeListener,
  AuthMode,
  AuthPrincipal,
  AuthProvider,
} from "./types";

export { CognitoAuthProvider, LocalAuthProvider };

/**
 * Build the `AuthProvider` matching the build-time deploy mode.
 *
 * Reads `VITE_AEGIS_DEPLOY_MODE` ("local" — default — or "cloud").
 * In Cloud mode, also requires the three Cognito env vars listed in
 * CognitoAuthProvider.ts; the constructor throws if they're missing
 * so a misconfigured Cloud build fails loudly at app startup rather
 * than silently falling through to no-auth.
 *
 * Single-shot: production code calls this once at module load time
 * and stores the result. Re-invoking creates a fresh provider, which
 * for the Cognito case creates a fresh UserManager — wasteful but
 * harmless.
 */
export function pickAuthProvider(): AuthProvider {
  const mode = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
    | "local"
    | "cloud";
  return mode === "cloud" ? new CognitoAuthProvider() : new LocalAuthProvider();
}
