// frontend_web/src/providers/AuthProvider/types.ts
//
// The AuthProvider port (ARCH §5 hexagonal) and the Principal it
// produces. Two implementations live in this directory:
//
//   - LocalAuthProvider — Local mode (ADR-0007): no-op identity,
//     synthetic "local" principal so the rest of the app code path
//     stays uniform regardless of deploy mode.
//   - CognitoAuthProvider — Cloud mode: AWS Cognito Hosted UI via
//     oidc-client-ts (Authorization Code + PKCE). Cognito User Pool
//     URL + client ID are config-injected via Vite env (see
//     CognitoAuthProvider.ts for the env-var contract).
//
// The Gateway-side mirror of `AuthPrincipal` is `auth.Principal` in
// `gateway_go/internal/auth/auth.go`; the same UserID / TenantID /
// Mode shape on both ends means a token issued by Cognito carries
// the claims the gateway's `auth.StaticJWTProvider` (or future real
// Cognito JWKS validator) reads back. One contract end-to-end.

/**
 * Tag distinguishing the two deploy flavors. Cheap to branch on
 * when behavior diverges (e.g. Cloud-only tenant isolation checks).
 */
export type AuthMode = "local" | "cloud";

/**
 * The authenticated caller identity propagated through the app.
 * Mirror of `auth.Principal` on the Gateway side.
 *
 * Fields are deliberately sparse — this is the minimum identity the
 * frontend needs; per-feature authorization (e.g. "only the host can
 * end the meeting") layers on top.
 */
export interface AuthPrincipal {
  /** Stable per-user identifier. Cognito `sub` claim in Cloud mode;
   *  the literal "local" in Local mode. */
  readonly userId: string;

  /** Tenant / organization identifier scoping session visibility.
   *  Cognito `custom:tenant_id` claim in Cloud mode; empty string in
   *  Local mode. */
  readonly tenantId: string;

  /** Which deploy flavor produced this principal. */
  readonly mode: AuthMode;

  /** Optional display fields, populated when the IdP provides them. */
  readonly displayName?: string;
  readonly email?: string;
}

/**
 * Subscriber callback fired when the auth state transitions
 * (signed-in ↔ signed-out, or a refresh updates the principal).
 */
export type AuthChangeListener = (principal: AuthPrincipal | null) => void;

/**
 * The port. Implementations honor a small synchronous-read +
 * asynchronous-mutate contract: anything React renders (header
 * "signed in as …") reads through the synchronous getters; sign-in
 * / sign-out are async because the underlying flow involves
 * navigation or network.
 */
export interface AuthProvider {
  /** Returns the current Principal, or null if signed out. */
  getPrincipal(): AuthPrincipal | null;

  /**
   * Returns the bearer token to attach to gateway API calls, or
   * null if signed out / not applicable. Local mode always returns
   * null; Cloud mode returns the current Cognito access token.
   *
   * Called per-request by the gateway-client interceptor; cheap.
   */
  getAccessToken(): string | null;

  /**
   * Initiates sign-in.
   *   - Local: synchronous-effective; resolves with the synthetic
   *     local principal already attached.
   *   - Cloud: navigates to the Cognito Hosted UI. Returns a never-
   *     resolving promise (the browser leaves the page). Caller
   *     handles the post-redirect state via `handleSignInCallback`
   *     on the /auth/callback route.
   */
  signIn(): Promise<void>;

  /** Clear session state and any stored tokens. Cloud mode also
   *  navigates to Cognito's logout endpoint. */
  signOut(): Promise<void>;

  /**
   * Process the OIDC redirect callback (Cloud mode). Mount on the
   * `/auth/callback` route. Reads `code` + `state` from the URL,
   * exchanges them for tokens, and resolves once the session is
   * established. Local mode is a no-op so callers don't need a
   * mode-aware mount.
   */
  handleSignInCallback(): Promise<void>;

  /**
   * Subscribe to principal changes. Returns an unsubscribe fn.
   * Use from React `useEffect` so a sign-in elsewhere in the app
   * propagates without a refresh.
   */
  onChange(listener: AuthChangeListener): () => void;
}
