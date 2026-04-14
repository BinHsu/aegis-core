// frontend_web/src/providers/AuthProvider/LocalAuthProvider.ts
//
// Local-mode AuthProvider — ADR-0007 specifies single-tenant
// single-user with no identity provider. We still produce a
// well-formed Principal so downstream code (React rendering, the
// gateway-client transport interceptor) doesn't need a "did auth
// run?" branch.
//
// The Local Principal is a process-scoped constant; subscribers fire
// once on attach and never again. signIn / signOut / handleSignInCallback
// are all no-ops (the Principal is always present).

import type { AuthChangeListener, AuthPrincipal, AuthProvider } from "./types";

const LOCAL_PRINCIPAL: AuthPrincipal = {
  userId: "local",
  tenantId: "",
  mode: "local",
  displayName: "Local user",
};

export class LocalAuthProvider implements AuthProvider {
  getPrincipal(): AuthPrincipal | null {
    return LOCAL_PRINCIPAL;
  }

  /** Local mode never carries an Authorization header — the gateway's
   *  NoOpProvider stamps the synthetic principal server-side. */
  getAccessToken(): string | null {
    return null;
  }

  /** No-op. The principal is already present. */
  async signIn(): Promise<void> {
    /* nothing to do */
  }

  /** No-op. There is no session to tear down. */
  async signOut(): Promise<void> {
    /* nothing to do */
  }

  /** No-op. Local mode has no redirect callback. Implemented for
   *  interface conformance so the App-level callback route doesn't
   *  need a mode check. */
  async handleSignInCallback(): Promise<void> {
    /* nothing to do */
  }

  /** Fires the listener once with the local principal, then never
   *  again. Returns an unsubscribe that's safe to call. */
  onChange(listener: AuthChangeListener): () => void {
    // Defer to a microtask so the caller has finished setting up
    // any state machinery before the synchronous-effective fire.
    void Promise.resolve().then(() => listener(LOCAL_PRINCIPAL));
    return () => {
      /* no subscriber state to clean up */
    };
  }
}
