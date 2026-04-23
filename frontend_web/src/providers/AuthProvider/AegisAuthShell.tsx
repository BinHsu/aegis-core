// frontend_web/src/providers/AuthProvider/AegisAuthShell.tsx
//
// Top-level auth wrapper per ADR-0034 §D2 (`react-oidc-context`
// adoption, Phase 4e-2 refactor). Mounted ONCE around the router in
// main.tsx; mode-branches the React tree:
//
//   Cloud mode  → <OidcAuthProvider userManager={cloudUserManager}>
//                     <CloudAegisAdapter> (maps oidc → AegisAuthContext)
//                        {children}
//                     </CloudAegisAdapter>
//                 </OidcAuthProvider>
//
//   Local mode  → {children}     (AegisAuthContext default value
//                                 is the LOCAL_VALUE static shape)
//
// Consumers call `useAegisAuth()` regardless of mode and receive a
// uniform `{ principal, loading, signIn, signOut }` shape — no
// per-consumer mode branching.
//
// The UserManager is imported from `@/lib/auth`; passing it in here
// keeps module-level code (gateway-client token interceptor) and
// React-level code (oidc hooks) reading the same oidc-client-ts
// state.

import {
  AuthProvider as OidcAuthProvider,
  useAuth as useOidcAuth,
} from "react-oidc-context";
import type { User } from "oidc-client-ts";
import {
  createContext,
  type ReactElement,
  type ReactNode,
  useContext,
  useMemo,
} from "react";

import { cloudUserManager } from "@/lib/auth";

import type { AuthPrincipal } from "./types";

const DEPLOY_MODE = (import.meta.env["VITE_AEGIS_DEPLOY_MODE"] ?? "local") as
  | "local"
  | "cloud";

const TENANT_CLAIM = "custom:tenant_id";

/**
 * What React components get from `useAegisAuth()`. Keeps the surface
 * small — components that need raw oidc-client-ts features
 * (access_token in-process, id_token, profile.email_verified) can
 * reach for `useAuth()` from react-oidc-context directly in Cloud
 * mode.
 */
export interface AegisAuthContextValue {
  /** Current principal, or null if signed out / still resolving. */
  readonly principal: AuthPrincipal | null;
  /** True while Cognito is doing its initial hydration or a
   *  silent-renew round-trip. Always false in Local mode. */
  readonly loading: boolean;
  /** Initiate sign-in. Cloud: navigates to Cognito Hosted UI (never
   *  resolves, by the time it would the page is gone). Local: no-op. */
  signIn: () => Promise<void>;
  /** Clear session + Cognito logout redirect (Cloud) or no-op (Local). */
  signOut: () => Promise<void>;
}

const LOCAL_PRINCIPAL: AuthPrincipal = {
  userId: "local",
  tenantId: "",
  mode: "local",
  displayName: "Local user",
};

const LOCAL_VALUE: AegisAuthContextValue = {
  principal: LOCAL_PRINCIPAL,
  loading: false,
  signIn: async (): Promise<void> => {
    /* no-op — local principal is always present */
  },
  signOut: async (): Promise<void> => {
    /* no-op — no session to tear down */
  },
};

// Default context value is the LOCAL shape so a local-mode tree that
// skips the Cloud wrappers still delivers a well-formed Principal to
// `useAegisAuth()` consumers.
const AegisAuthContext = createContext<AegisAuthContextValue>(LOCAL_VALUE);

/**
 * Mount once around the router in main.tsx. Branches on build-time
 * `VITE_AEGIS_DEPLOY_MODE` to either wire up react-oidc-context (and
 * adapt it to our context shape) or pass through unchanged with the
 * static local context value.
 */
export function AegisAuthShell({
  children,
}: {
  readonly children: ReactNode;
}): ReactElement {
  if (DEPLOY_MODE === "cloud") {
    if (cloudUserManager === null) {
      // Defensive: `lib/auth.ts` constructs the UserManager when
      // DEPLOY_MODE is "cloud", so reaching here means the env-var
      // wiring is inconsistent (e.g. vite bundled one value, runtime
      // got another). Fail loudly rather than render a half-mode UI.
      throw new Error(
        "AegisAuthShell: DEPLOY_MODE=cloud but cloudUserManager is null. " +
          "Check VITE_AEGIS_DEPLOY_MODE and VITE_AEGIS_COGNITO_* env vars.",
      );
    }
    return (
      <OidcAuthProvider userManager={cloudUserManager}>
        <CloudAegisAdapter>{children}</CloudAegisAdapter>
      </OidcAuthProvider>
    );
  }
  // Local mode: no wrapping needed. `useAegisAuth()` hits the default
  // context value below.
  return <>{children}</>;
}

/**
 * Cloud-mode adapter. Reads from react-oidc-context's `useAuth()` and
 * re-exposes the subset we care about under AegisAuthContext.
 */
function CloudAegisAdapter({
  children,
}: {
  readonly children: ReactNode;
}): ReactElement {
  const oidc = useOidcAuth();

  const value = useMemo<AegisAuthContextValue>(() => {
    const principal =
      oidc.isAuthenticated && oidc.user && !oidc.user.expired
        ? userToPrincipal(oidc.user)
        : null;
    return {
      principal,
      loading: oidc.isLoading,
      signIn: () => oidc.signinRedirect(),
      signOut: () => oidc.signoutRedirect(),
    };
  }, [oidc]);

  return (
    <AegisAuthContext.Provider value={value}>
      {children}
    </AegisAuthContext.Provider>
  );
}

/**
 * Access the current auth state from any React component under
 * <AegisAuthShell>. Mode-agnostic — consumers don't need to branch on
 * DEPLOY_MODE; the shape is uniform.
 */
export function useAegisAuth(): AegisAuthContextValue {
  return useContext(AegisAuthContext);
}

/**
 * Map an oidc-client-ts User into the Aegis AuthPrincipal shape.
 * Exported so tests can exercise it without constructing a full
 * UserManager fixture.
 */
export function userToPrincipal(user: User): AuthPrincipal {
  const profile = user.profile;
  const userId = profile.sub;
  const tenantClaim = (profile as Record<string, unknown>)[TENANT_CLAIM];
  const tenantId = typeof tenantClaim === "string" ? tenantClaim : "";

  const principal: AuthPrincipal = {
    userId,
    tenantId,
    mode: "cloud",
  };
  if (typeof profile.name === "string" && profile.name !== "") {
    return { ...principal, displayName: profile.name };
  }
  if (typeof profile.email === "string" && profile.email !== "") {
    return { ...principal, email: profile.email };
  }
  return principal;
}
