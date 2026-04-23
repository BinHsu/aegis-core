// frontend_web/src/lib/auth.ts
//
// Module-level auth state per ADR-0034 §D2, after the 4e-2 refactor to
// `react-oidc-context`. Two responsibilities:
//
//   1. In Cloud mode, own the `oidc-client-ts` UserManager — the
//      single source of truth that both module-level code
//      (gateway-client token interceptor) AND the React tree
//      (react-oidc-context's <AuthProvider userManager={...}>) share.
//
//   2. Wire a synchronous token getter into the gateway-client
//      interceptor via `setAuthTokenGetter`. The interceptor runs
//      per-RPC and cannot await; we keep a cached User that's
//      updated via UserManager events.
//
// In Local mode the UserManager is never constructed; the token
// getter returns null so the gateway-client omits the Authorization
// header (gateway-side NoOpProvider stamps a synthetic principal).

import { type User, UserManager, WebStorageStateStore } from "oidc-client-ts";

import { setAuthTokenGetter } from "./gateway-client";

const DEPLOY_MODE = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
  | "local"
  | "cloud";

/**
 * The singleton UserManager for Cloud mode. Null in Local mode.
 *
 * Exported for <AuthProvider userManager={...}> in AegisAuthShell —
 * sharing the instance keeps module-level code (token getter below)
 * and React-level code (react-oidc-context's useAuth()) reading the
 * same session state.
 */
export const cloudUserManager: UserManager | null =
  DEPLOY_MODE === "cloud" ? buildCloudUserManager() : null;

/**
 * Latest authenticated user, refreshed from UserManager events. The
 * gateway-client interceptor reads through `getAccessToken()` below
 * which inspects this — keeping it in module scope avoids an async
 * `getUser()` per RPC.
 */
let cachedUser: User | null = null;

if (cloudUserManager) {
  // Hydrate from any persisted session (user landed on the page with
  // a valid Cognito session still in localStorage).
  void cloudUserManager.getUser().then((u) => {
    cachedUser = u && !u.expired ? u : null;
  });
  cloudUserManager.events.addUserLoaded((u) => {
    cachedUser = u;
  });
  cloudUserManager.events.addUserUnloaded(() => {
    cachedUser = null;
  });
  cloudUserManager.events.addAccessTokenExpired(() => {
    cachedUser = null;
  });
}

setAuthTokenGetter(() => {
  if (!cachedUser || cachedUser.expired) return null;
  return cachedUser.access_token ?? null;
});

/**
 * Process the OIDC redirect callback. Called from AuthCallbackPage
 * on the `/auth/callback` route. Cloud mode: exchanges the
 * ?code=&state= query params for tokens via the UserManager. Local
 * mode: no-op (the router still sends users here for URL uniformity,
 * but there's nothing to do).
 *
 * Kept as an imperative helper rather than relying on
 * react-oidc-context's onSigninCallback prop because the callback
 * page needs to navigate to /host after completion — imperative
 * control makes the sequence (process → navigate) obvious at the
 * consumer.
 */
export async function handleSignInCallback(): Promise<void> {
  if (cloudUserManager === null) return;
  await cloudUserManager.signinRedirectCallback();
}

function buildCloudUserManager(): UserManager {
  const env = import.meta.env;
  const authority = env["VITE_AEGIS_COGNITO_AUTHORITY"];
  const clientId = env["VITE_AEGIS_COGNITO_CLIENT_ID"];
  const redirectUri = env["VITE_AEGIS_COGNITO_REDIRECT_URI"];
  const logoutUriEnv = env["VITE_AEGIS_COGNITO_LOGOUT_URI"];
  if (!authority || !clientId || !redirectUri) {
    throw new Error(
      "lib/auth: missing required Cognito env vars. Need " +
        "VITE_AEGIS_COGNITO_AUTHORITY, VITE_AEGIS_COGNITO_CLIENT_ID, " +
        "VITE_AEGIS_COGNITO_REDIRECT_URI. (Set in .env.local for dev or " +
        "pass via the build step in CI.)",
    );
  }
  // Empty-string unset protection — a .env with the var declared but
  // blank (`VITE_AEGIS_COGNITO_LOGOUT_URI=`) should behave the same as
  // omitting the line.
  const logoutUri =
    logoutUriEnv && logoutUriEnv !== "" ? logoutUriEnv : undefined;

  return new UserManager({
    authority,
    client_id: clientId,
    redirect_uri: redirectUri,
    // Cognito's logout endpoint expects a `logout_uri` query param
    // matching one of the app client's logout_urls — oidc-client-ts
    // populates this from post_logout_redirect_uri. LDZ's Terraform
    // registers the strawman per aegis-core#76.
    ...(logoutUri ? { post_logout_redirect_uri: logoutUri } : {}),
    response_type: "code",
    scope: "openid profile email",
    // WebStorageStateStore(localStorage) persists the User so a
    // page reload during a meeting doesn't lose the session. ADR-0034
    // §D2 originally called for memory-only storage to mitigate XSS,
    // but Phase 3 SPA work landed localStorage with an explicit
    // comment on the tradeoff; preserved here because the 4e-2
    // refactor's goal is library swap, not security-policy change.
    // When LDZ's Cognito pool goes live, revisit this choice in a
    // dedicated ADR revision.
    userStore: new WebStorageStateStore({ store: window.localStorage }),
    automaticSilentRenew: true,
  });
}
