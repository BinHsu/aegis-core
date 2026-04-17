// frontend_web/src/providers/AuthProvider/CognitoAuthProvider.ts
//
// Cloud-mode AuthProvider backed by AWS Cognito's Hosted UI via the
// `oidc-client-ts` library (OIDC Authorization Code + PKCE flow).
//
// Why Hosted UI rather than rolling our own form:
//   - Password never touches our frontend → smaller attack surface.
//   - MFA, password reset, social login, account recovery all served
//     by AWS without UI work on our side.
//   - Standard OIDC under the hood — same code can talk to Auth0,
//     Okta, or Keycloak by changing only the `authority` config.
//
// Required environment variables (set at `vite build` time):
//   - `VITE_AEGIS_COGNITO_AUTHORITY`     e.g.
//       "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_AbCdEfGhI"
//   - `VITE_AEGIS_COGNITO_CLIENT_ID`     e.g. "abcdefghijklmnop1234567890"
//   - `VITE_AEGIS_COGNITO_REDIRECT_URI`  e.g.
//       "https://app.aegis.example/auth/callback" (prod)
//       "http://localhost:5173/auth/callback"     (dev)
//
// The User Pool itself is provisioned in the `aegis-aws-landing-zone` repo
// (separate session, see `docs/interview-notes.md`); this code only
// needs the URL + client ID at build time.

import { type User, UserManager, WebStorageStateStore } from "oidc-client-ts";

import type { AuthChangeListener, AuthPrincipal, AuthProvider } from "./types";

/**
 * Cognito custom-claim conventions used in our User Pool. Mirrors the
 * gateway-side `auth.StaticJWTProvider` defaults
 * (`gateway_go/internal/auth/jwt.go`) so what the IdP issues is what
 * the gateway parses.
 */
const TENANT_CLAIM = "custom:tenant_id";

interface CognitoConfig {
  readonly authority: string;
  readonly clientId: string;
  readonly redirectUri: string;
}

function readConfig(): CognitoConfig {
  const env = import.meta.env;
  const authority = env["VITE_AEGIS_COGNITO_AUTHORITY"];
  const clientId = env["VITE_AEGIS_COGNITO_CLIENT_ID"];
  const redirectUri = env["VITE_AEGIS_COGNITO_REDIRECT_URI"];
  if (!authority || !clientId || !redirectUri) {
    throw new Error(
      "CognitoAuthProvider: missing required env. Need " +
        "VITE_AEGIS_COGNITO_AUTHORITY, VITE_AEGIS_COGNITO_CLIENT_ID, " +
        "VITE_AEGIS_COGNITO_REDIRECT_URI. (Set these in .env.local for dev " +
        "or pass via the build step in CI.)",
    );
  }
  return { authority, clientId, redirectUri };
}

export class CognitoAuthProvider implements AuthProvider {
  private readonly userManager: UserManager;
  private currentUser: User | null = null;
  private listeners = new Set<AuthChangeListener>();

  constructor() {
    const cfg = readConfig();
    this.userManager = new UserManager({
      authority: cfg.authority,
      client_id: cfg.clientId,
      redirect_uri: cfg.redirectUri,
      response_type: "code",
      scope: "openid profile email",
      // Persist tokens in localStorage so a page reload during a meeting
      // doesn't lose the session. WebStorageStateStore is the
      // oidc-client-ts default but we declare it explicitly so the
      // posture is reviewable. Tokens live under the
      // "oidc.user:<authority>:<client_id>" key.
      userStore: new WebStorageStateStore({ store: window.localStorage }),
      // The User Pool's discovery doc handles auto-refresh; we just
      // opt in to the silent renew that ships with oidc-client-ts.
      automaticSilentRenew: true,
    });

    // Hydrate from any existing session left in localStorage.
    void this.userManager.getUser().then((u) => {
      this.currentUser = u && !u.expired ? u : null;
      this.fire();
    });

    // Wire the user-manager events to our subscribers. signOut /
    // silent-renew failures both nullify the principal.
    this.userManager.events.addUserLoaded((u) => {
      this.currentUser = u;
      this.fire();
    });
    this.userManager.events.addUserUnloaded(() => {
      this.currentUser = null;
      this.fire();
    });
    this.userManager.events.addAccessTokenExpired(() => {
      this.currentUser = null;
      this.fire();
    });
  }

  getPrincipal(): AuthPrincipal | null {
    if (!this.currentUser || this.currentUser.expired) {
      return null;
    }
    return userToPrincipal(this.currentUser);
  }

  getAccessToken(): string | null {
    if (!this.currentUser || this.currentUser.expired) {
      return null;
    }
    return this.currentUser.access_token ?? null;
  }

  async signIn(): Promise<void> {
    // Navigates the page to the Cognito Hosted UI. The promise is
    // logically never-resolving — by the time it would, the page
    // is gone.
    await this.userManager.signinRedirect();
  }

  async signOut(): Promise<void> {
    // signoutRedirect navigates to Cognito's logout endpoint, which
    // clears the IdP session and (typically) returns the user to
    // their landing page. Falls back to a local-only sign-out if
    // the user-manager has no session to redirect from.
    if (this.currentUser) {
      await this.userManager.signoutRedirect();
    } else {
      await this.userManager.removeUser();
    }
  }

  async handleSignInCallback(): Promise<void> {
    // Reads `code` and `state` query params, exchanges code for
    // tokens via Cognito's token endpoint, persists the User to
    // localStorage. The `addUserLoaded` event handler above picks
    // it up and notifies subscribers.
    await this.userManager.signinRedirectCallback();
  }

  onChange(listener: AuthChangeListener): () => void {
    this.listeners.add(listener);
    // Fire immediately with the current state so subscribers don't
    // have to query separately.
    listener(this.getPrincipal());
    return () => {
      this.listeners.delete(listener);
    };
  }

  private fire(): void {
    const p = this.getPrincipal();
    for (const listener of this.listeners) {
      listener(p);
    }
  }
}

function userToPrincipal(user: User): AuthPrincipal {
  const profile = user.profile;
  // Cognito's `sub` claim is the canonical user identifier.
  // `profile.sub` is typed `string` (required by OIDC spec).
  const userId = profile.sub;
  // Custom claim — typed `unknown` because OIDC profile is open-shape.
  // Coerce defensively; an absent / non-string custom claim becomes
  // empty string (matches the Local mode "no tenant" convention).
  const tenantClaim = (profile as Record<string, unknown>)[TENANT_CLAIM];
  const tenantId = typeof tenantClaim === "string" ? tenantClaim : "";

  const principal: AuthPrincipal = {
    userId,
    tenantId,
    mode: "cloud",
  };
  // Optional fields — omit if absent so the consumer's
  // exactOptionalPropertyTypes-strict consumers see `undefined`
  // rather than an empty string they have to defensively handle.
  if (typeof profile.name === "string" && profile.name !== "") {
    return { ...principal, displayName: profile.name };
  }
  if (typeof profile.email === "string" && profile.email !== "") {
    return { ...principal, email: profile.email };
  }
  return principal;
}
