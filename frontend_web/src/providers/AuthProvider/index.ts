// frontend_web/src/providers/AuthProvider/index.ts
//
// Public surface of the AuthProvider port, post-4e-2 refactor to
// `react-oidc-context`. Consumers import two things from here:
//
//   - <AegisAuthShell>         — mount once in main.tsx around the
//                                router; branches Cloud vs Local
//                                deploy mode internally.
//   - useAegisAuth()           — React hook returning
//                                { principal, loading, signIn, signOut }
//                                regardless of deploy mode.
//
// The `AuthPrincipal` + `AuthMode` types are also re-exported here
// since every consumer that needs them also imports one of the
// above; avoids a second `./types` import line per consumer.
//
// The class-based `CognitoAuthProvider` / `LocalAuthProvider` /
// `pickAuthProvider` trio that used to live here was superseded
// by the react-oidc-context wrapper — see ADR-0034 §D2 and the
// 4e-2 refactor commit for the rationale.

export {
  AegisAuthShell,
  useAegisAuth,
  userToPrincipal,
} from "./AegisAuthShell";
export type { AegisAuthContextValue } from "./AegisAuthShell";
export type { AuthMode, AuthPrincipal } from "./types";
