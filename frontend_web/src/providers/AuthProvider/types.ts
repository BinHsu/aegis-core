// frontend_web/src/providers/AuthProvider/types.ts
//
// Shared auth types. After the react-oidc-context refactor (Phase
// 4e-2), the AuthProvider port is no longer a class we implement
// ourselves — `react-oidc-context` owns the UserManager lifecycle and
// exposes its own hook. This file keeps only the two types the rest of
// the app code pins against: the deploy-mode tag and the Principal
// shape (mirror of `gateway_go/internal/auth.Principal`).

/**
 * Tag distinguishing the two deploy flavors. Cheap to branch on when
 * behavior diverges (e.g. Cloud-only tenant isolation checks).
 */
export type AuthMode = "local" | "cloud";

/**
 * The authenticated caller identity propagated through the app.
 * Mirror of `auth.Principal` on the Gateway side.
 *
 * Fields are deliberately sparse — this is the minimum identity the
 * frontend needs; per-feature authorization (e.g. "only the host can
 * end the meeting") layers on top.
 *
 * Produced by `useAegisAuth()` (see AegisAuthShell.tsx) — in Cloud
 * mode it's derived from the Cognito ID token's claims; in Local mode
 * it's the synthetic "local" principal.
 */
export interface AuthPrincipal {
  /** Stable per-user identifier. Cognito `sub` claim in Cloud mode;
   *  the literal "local" in Local mode. */
  readonly userId: string;

  /** Tenant / organization identifier scoping session visibility.
   *  Cognito `custom:tenant_id` claim in Cloud mode (immutable per
   *  aegis-core#76 2026-04-23); empty string in Local mode. */
  readonly tenantId: string;

  /** Which deploy flavor produced this principal. */
  readonly mode: AuthMode;

  /** Optional display fields, populated when the IdP provides them. */
  readonly displayName?: string;
  readonly email?: string;
}
