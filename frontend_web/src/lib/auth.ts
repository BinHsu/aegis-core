// frontend_web/src/lib/auth.ts
//
// Module-level auth state per ADR-0034 §D2 (react-oidc-context). Two
// responsibilities:
//
//   1. In Cloud mode, own the `oidc-client-ts` UserManager — the single
//      source of truth that both module-level code (gateway-client token
//      interceptor) AND the React tree (react-oidc-context's
//      <AuthProvider userManager={...}>) share.
//
//   2. Wire a synchronous token getter into the gateway-client
//      interceptor via setAuthTokenGetter. The interceptor runs per-RPC
//      and cannot await; we keep a cached User updated via UserManager
//      events.
//
// Lifecycle (ADR-15): the UserManager is built from RUNTIME config in
// initAuth(cfg) — called once from main.tsx after loadConfig() — not at
// module-load time. Pre-refactor this was a module-const that read
// import.meta.env, which baked the Cognito settings into the bundle and
// threw synchronously at import if they were missing.
//
// In Local mode the UserManager is never constructed; the token getter
// is cleared so the gateway-client omits the Authorization header
// (gateway-side NoOpProvider stamps a synthetic principal).

import { type User, UserManager, WebStorageStateStore } from "oidc-client-ts";

import { type AppConfig, type CognitoConfig } from "./config";
import { setAuthTokenGetter } from "./gateway-client";

/** The singleton UserManager for Cloud mode. Null until initAuth runs
 *  in Cloud mode; stays null in Local mode. */
let cloudUserManager: UserManager | null = null;

/**
 * Accessor for the Cloud UserManager. Exported for
 * <AuthProvider userManager={...}> in AegisAuthShell — sharing the
 * instance keeps module-level code (token getter below) and React-level
 * code (react-oidc-context's useAuth()) reading the same session state.
 */
export function getCloudUserManager(): UserManager | null {
  return cloudUserManager;
}

/** Latest authenticated user, refreshed from UserManager events. The
 *  gateway-client interceptor reads through the registered getter which
 *  inspects this — module scope avoids an async getUser() per RPC. */
let cachedUser: User | null = null;

/**
 * Initialize auth from runtime config. Cloud mode builds the
 * UserManager, hydrates any persisted session, and registers the token
 * getter. Local mode clears the getter (no Authorization header). Call
 * once from main.tsx after loadConfig().
 */
export function initAuth(cfg: AppConfig): void {
  if (cfg.deployMode !== "cloud" || cfg.cognito === null) {
    setAuthTokenGetter(null);
    return;
  }

  cloudUserManager = buildCloudUserManager(cfg.cognito);

  // Hydrate from any persisted session (user landed with a valid
  // Cognito session still in localStorage).
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

  setAuthTokenGetter(() => {
    if (!cachedUser || cachedUser.expired) return null;
    // Send the ID token, not the access token. The gateway's OIDCProvider
    // validates `aud == app client id` and reads `custom:tenant_id` — both of
    // which Cognito puts only on the ID token. A Cognito access token carries
    // the user-pool id as `aud` and no custom attributes, so the gateway would
    // 401 every request (see ADR-0034 §D2 and oidc_integration_test.go).
    return cachedUser.id_token ?? null;
  });
}

/**
 * Process the OIDC redirect callback. Called from AuthCallbackPage on
 * the `/auth/callback` route. Cloud mode: exchanges the ?code=&state=
 * query params for tokens via the UserManager. Local mode (or before
 * initAuth): no-op.
 *
 * Kept imperative rather than relying on react-oidc-context's
 * onSigninCallback prop because the callback page needs to navigate to
 * /host after completion — imperative control makes the sequence
 * (process → navigate) obvious at the consumer.
 */
export async function handleSignInCallback(): Promise<void> {
  if (cloudUserManager === null) return;
  await cloudUserManager.signinRedirectCallback();
}

function buildCloudUserManager(cognito: CognitoConfig): UserManager {
  const { authority, clientId, redirectUri, logoutUri } = cognito;
  return new UserManager({
    authority,
    client_id: clientId,
    redirect_uri: redirectUri,
    // Cognito's logout endpoint expects a `logout_uri` query param
    // matching one of the app client's logout_urls — oidc-client-ts
    // populates this from post_logout_redirect_uri.
    ...(logoutUri ? { post_logout_redirect_uri: logoutUri } : {}),
    response_type: "code",
    scope: "openid profile email",
    // WebStorageStateStore(localStorage) persists the User so a page
    // reload during a meeting doesn't lose the session. ADR-0034 §D2
    // originally called for memory-only storage to mitigate XSS; Phase 3
    // landed localStorage with an explicit tradeoff comment. Preserved
    // here — this refactor's goal is the config source, not the
    // security-policy choice. Revisit in a dedicated ADR when the
    // Cognito pool goes live.
    userStore: new WebStorageStateStore({ store: window.localStorage }),
    automaticSilentRenew: true,
  });
}
