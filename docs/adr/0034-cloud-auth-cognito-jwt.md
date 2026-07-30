# ADR-0034: Cloud-mode authentication — Cognito JWT consumption contract

| Field      | Value |
| ---------- | ----- |
| Status     | Accepted — 2026-04-23. Phase 4e implementation slices added to ROADMAP.md on acceptance. |
| Date       | 2026-04-23 |
| Deciders   | Project author |
| Context    | LDZ committed via `aegis-landing-zone-aws` ADR-026 (cross-repo issue aegis-core#76, Partially Accepted 2026-04-23) to provision a Cognito User Pool in `staging/auth/`. This ADR records how aegis-core consumes that pool: gateway JWT validation, SPA OAuth flow, `custom:tenant_id` propagation into `ADR-0022`'s multi-tenancy filter. |
| Related    | ADR-0001 (CreateMeeting + session tokens), ADR-0022 (multi-tenancy via `custom:tenant_id`), ADR-0020 (engine owns inference, not auth), ADR-0031 (LOCAL mode escape-hatch pattern), aegis-core #76 (consumption contract thread), aegis-landing-zone-aws PR #138 (ADR-026 Cognito commitment) |
| Supersedes | Phase 2 `StaticJWTProvider`'s CLOUD path usage. `StaticJWTProvider` itself remains for integration-test scenarios that need pre-shared-secret tokens. LOCAL `NoOpProvider` unchanged. |

## Context

Phase 3 LAN demo (`v0.1.0-demo-lan`, 2026-04-22) closed the image-readiness gate on aegis-core #58. Cold-apply cloud loop however is gated on caller identity — `gateway_go/internal/auth/jwt.go`'s `StaticJWTProvider` (Phase 2 A2) validates HS256 pre-shared-secret tokens, which is integration-test grade, not federated identity from a real User Pool.

LDZ now commits via cross-repo #76 to provision a Cognito User Pool with:
- Hosted UI OAuth / PKCE flow (SPA-driven)
- ID tokens signed **RS256**, verified via Cognito's JWKS endpoint
- Single custom attribute `custom:tenant_id`, declared immutable (naming locked via #76 comment 2026-04-23 to avoid pool-recreate cost)
- Default token lifetimes (1h access / 30d refresh)
- Global logout endpoint

This ADR closes the aegis-core side of that contract. What it does **not** cover:

- Cognito pool Terraform — LDZ scope, tracked in ldz ADR-026.
- Federation with external IdPs (Google / GitHub / SAML) — MVP scope is local Cognito users only per #76.
- Fine-grained RBAC via `cognito:groups` — preserved as future claim axis per ADR-0022 §"Phase 4 implementation note — re-evaluate claim set before finalizing". Not MVP.
- Engine-side auth — per ADR-0020, engine does not decode tokens; it consumes `Principal.TenantID` / `UserID` from gateway-set gRPC metadata.

## Decisions

### D1. Gateway JWT validation — `lestrrat-go/jwx/v2` + in-memory JWKS cache

**Pick**: `github.com/lestrrat-go/jwx/v2` for JWKS fetching, caching, and JWT verification. Matches the library pre-selected in `ROADMAP.md` Phase 2 Known Gaps (*"drop in `github.com/lestrrat-go/jwx/v2` (JWKS client + caching)"*).

Concretely:

- `jwk.Cache` maintains the JWKS set keyed off the Cognito issuer URL (`https://cognito-idp.<region>.amazonaws.com/<pool-id>`), refreshed every **15 minutes** (Cognito rotates signing keys on the order of months; 15min is conservative with low cost).
- `jwt.Parse(tokenBytes, jwt.WithKeySet(...))` validates signature + `exp` + `iss` + `aud` (app client ID).
- New `auth.OIDCProvider` implementing the existing `auth.Provider` interface at `gateway_go/internal/auth/auth.go:69-71`, slotted into the `cmd/gateway/main.go` factory selected by `DEPLOY_MODE=cloud`.
- Failure handling: return `codes.Unauthenticated` via the existing interceptor; structured log with the failure category (signature / expired / missing claim / JWKS fetch error) but **never** the token bytes.

Claim → `Principal` mapping (aligns with existing `Principal` shape at `auth.go:47-58`):

- `sub` → `Principal.UserID`
- `custom:tenant_id` → `Principal.TenantID` (pinned naming per aegis-core #76 2026-04-23)
- `Principal.Mode` = `ModeCloud`

Issuer + audience verification are mandatory. A missing `custom:tenant_id` returns `Unauthenticated` — no silent fallback to empty `TenantID`, which would conflate with LOCAL-mode semantics.

### D2. SPA OAuth scaffold — `react-oidc-context` + Cognito Hosted UI

**Pick**: `react-oidc-context` (wraps `oidc-client-ts`) as the OAuth PKCE client in `frontend_web/`. Cognito Hosted UI serves the login page.

Concretely:

- Add `react-oidc-context` + `oidc-client-ts` via `./tools/scripts/frontend.sh install`.
- `<AuthProvider>` in `frontend_web/src/main.tsx` configured from Vite env vars (`VITE_COGNITO_AUTHORITY`, `VITE_COGNITO_CLIENT_ID`, `VITE_COGNITO_REDIRECT_URI`, `VITE_COGNITO_LOGOUT_URI`) — populated at build time from the K8s ConfigMap the gateway's SPA `index.html` reads. Values match the strawman LDZ committed to Terraform per #76:
    - `redirect_uri = https://aegis-app.staging.binhsu.org/auth/callback`
    - `post_logout_redirect_uri = https://aegis-app.staging.binhsu.org/`
- New React Router route `/auth/callback` that finalises the PKCE code-for-token exchange and routes to the post-login landing page.
- `useAuth()` hook replaces or augments `frontend_web/src/lib/auth.ts`, depending on the existing module shape. The existing `auth.ts` holds session-token storage for the CreateMeeting → JoinAsViewer flow and is orthogonal to Cognito ID-token storage; likely the two coexist (Cognito ID token for gateway auth, existing session token for viewer-join RPCs issued by the gateway itself per ADR-0001).
- Bearer injection: the existing `frontend_web/src/lib/gateway-client.ts` wrapper attaches `metadata: { Authorization: \`Bearer ${idToken}\` }` on every outbound gRPC-Web call. Interceptor-style, one change point.
- **Token storage: memory-only** — `oidc-client-ts` configured with `userStore: InMemoryWebStorage`. Rationale: mitigate XSS token exfiltration. Trade-off: silent-auth refresh on every SPA reload. If cold-apply UX surfaces friction, revisit — do not regress to `localStorage` without a compensating mitigation.

### D3. `custom:tenant_id` propagation path

**Pick**: reuse the existing wire. `Principal.TenantID` is populated by D1's `OIDCProvider`; ADR-0022 §"Query path" already specifies the gateway → engine gRPC-metadata hop and the engine-side Qdrant filter.

Concretely no new plumbing beyond D1:

- Gateway interceptor (`auth.UnaryInterceptor` / `auth.StreamInterceptor`) calls `Provider.Authenticate` → extracts claims → sets `Principal{UserID, TenantID, Mode}` → `WithPrincipal(ctx, p)`. This code exists today; D1 just gives it a real Cognito-aware `Provider`.
- Gateway → engine metadata: the engine-bound client in `gateway_go/internal/pipeline/` must forward `tenant_id` + `sub` per ADR-0022. Verify in 4e-3 that the forwarding is actually wired (may need a small change if the pipeline was built assuming LOCAL-mode only).
- Engine side: `AuthContext` interceptor reads metadata → populates `session.TenantID` → Qdrant search applies collection `aegis_<tenant_id>_<corpus>` + payload filter `must:[{key:"user_id", match:<sub>}]` per ADR-0022 §"Schema shape".

This ADR does **not** re-litigate ADR-0022's schema. It names ADR-0022 as the consumer spec and confirms that D1 + D2 satisfy its `custom:tenant_id` source assumption.

### D4. Testing strategy — layered (unit mock + integration + optional E2E)

Per CLAUDE.md Rule 2: each slice ships its regression test in the same PR.

**Unit (hermetic, PR-time CI)** — replaces nothing in `auth_test.go`; adds `oidc_provider_test.go`:

- `httptest.Server` serves a static RSA keypair's JWKS endpoint + synthesised `.well-known/openid-configuration`.
- Test helpers sign JWTs with the matching private key.
- `OIDCProvider` pointed at the mock issuer URL.
- Coverage: happy path, expired token, wrong audience, wrong issuer, missing `custom:tenant_id`, signature mismatch, JWKS fetch failure + refresh retry, JWKS key rotation mid-flight.

**Integration (gated on `AEGIS_COGNITO_*` env vars, nightly CI)**:

- Uses the Dev User Pool LDZ provisions as a side effect of #76 Terraform apply.
- Test driver creates a Dev user via `AdminCreateUser`, drives `AdminInitiateAuth` password flow, feeds the resulting real ID token through `OIDCProvider.Authenticate`, asserts the derived `Principal`.
- Runs nightly only — non-zero AWS cost + Cognito throttling risk on PR-time cadence.

**Playwright E2E (optional, lands after Phase 4 infra is standing)**:

- Spec drives Cognito Hosted UI → SPA sign-in → CreateMeeting → transcript → hint render on staging.
- Nightly-only; real value only after `staging/auth/` + ACM + Route53 + the cluster itself are all live.

## LOCAL mode posture

Per ADR-0031 LOCAL pattern, cloud-auth is a CLOUD-only additive layer:

| Component        | LOCAL mode                                      | CLOUD mode (this ADR)                                      |
| ---------------- | ----------------------------------------------- | ---------------------------------------------------------- |
| Gateway provider | `NoOpProvider` — synthetic `Principal{UserID:"local", TenantID:"", Mode:ModeLocal}` | `OIDCProvider` — Cognito JWKS + claim mapping (D1)         |
| SPA OAuth        | `<AuthProvider>` not mounted; no bearer header  | `<AuthProvider>` wraps app; bearer header on every RPC (D2) |
| Test escape hatch | no JWT involved                                 | `StaticJWTProvider` preserved for `DEPLOY_MODE=cloud-test` integration-test scenarios |

The `auth.Provider` factory in `cmd/gateway/main.go` selects by the `DEPLOY_MODE` env var:

- `DEPLOY_MODE=local` (default, Phase 3 LAN demo) → `NoOpProvider`
- `DEPLOY_MODE=cloud` → `OIDCProvider`
- `DEPLOY_MODE=cloud-test` → `StaticJWTProvider` (preserves pre-Cognito integration-test wiring)

Single binary, three modes, one env-var switch. No build-tag gating.

SPA mirrors via `VITE_DEPLOY_MODE`: LOCAL build skips `<AuthProvider>`; CLOUD build mounts it. Bundle-level gating so the OAuth library doesn't ship in LAN demos.

This parallels ADR-0031's mTLS LOCAL posture (plaintext on localhost) and ADR-0001's session-token local path (HS256 scaffold). Consistent escape-hatch pattern across three enterprise-layer concerns.

## Alternatives considered

### D1 alternatives

**`coreos/go-oidc`** (higher-level OIDC discovery):

- Auto-fetches `.well-known/openid-configuration`, cleaner "point-at-issuer" ergonomics.
- **Rejected** — ROADMAP's Phase 2 Known Gap pre-picked `jwx/v2`. The two libraries' verification outcomes are equivalent for Cognito's straight OIDC flow; reversing a prior pick without a load-bearing reason is churn. `jwx/v2`'s lower-level primitives also leave more room if future claim shapes (JWE-encrypted ID tokens, nested JWT) matter — unlikely but preserves the option.

**stdlib-only** (`crypto/rsa` + hand-rolled JWKS fetch + refresh loop):

- No third-party dep.
- **Rejected** — JWKS rotation, cache expiry, key-ID lookup, and thundering-herd avoidance are exactly the edge cases third-party libraries earn their keep on. Time is better spent on gateway business logic.

### D2 alternatives

**`@aws-amplify/ui-react`** (AWS's SPA auth component kit):

- Drop-in `<Authenticator>` UI, one-package Cognito integration.
- **Rejected** — AWS-specific; ~400KB bundle cost vs `oidc-client-ts`'s ~80KB; couples SPA tightly to Cognito. `react-oidc-context` is provider-agnostic so any future IdP move is a config change, not a rewrite.

**Hand-rolled PKCE** (raw `fetch` + `crypto.subtle`):

- Zero dep.
- **Rejected** — auth is the wrong place for NIH. Reference implementations carry the edge cases (clock drift, nonce validation, state CSRF, redirect-URI matching) custom code routinely misses.

### D3 alternatives

**Engine validates JWT directly** (Bearer forwarded to engine):

- **Rejected** — violates ADR-0020 "engine owns inference, not auth". Engine should carry neither OIDC config nor JWKS cache. Gateway is the trust boundary.

**HTTP header instead of gRPC metadata**:

- **Rejected** — engine RPC is gRPC-native (ADR-0006); metadata is the canonical transport. A parallel HTTP channel has no payoff.

### D4 alternatives

**Live Cognito for unit tests**:

- **Rejected** — breaks hermetic testing (CLAUDE.md Rule 6), adds CI cost, couples every PR to AWS availability. Mock JWKS gives the same coverage for signature / expiration / claim-extraction paths.

**No integration test** (trust unit + prod usage):

- **Rejected** — per CLAUDE.md Rule 2's test-first discipline, unit coverage is not sufficient for cross-process OIDC flow. The nightly integration test is the load-bearing escape-hatch for UT's cross-service blindspot.

## Consequences

### Positive

- Closes the "cloud auth integration" gap flagged by aegis-core #58 + #76 — cold-apply cloud loop becomes end-to-end viable once this ADR ships + LDZ's Terraform applies.
- ADR-0022's Qdrant tenant filter finally gets its real `tenant_id` source; `custom:tenant_id` pin (#76 2026-04-23) removes the Cognito pool-recreate risk.
- `auth.Provider` port already exists (Phase 2 A2) — adding `OIDCProvider` is purely additive. `StaticJWTProvider` is preserved for integration-test scenarios that need pre-shared-secret tokens.
- LOCAL mode unchanged. Phase 3 LAN demo continues to work; CLOUD auth is an additive layer.
- Library picks are swap-resistant — `Provider` port abstracts the IdP, `react-oidc-context` is provider-agnostic.

### Negative

- Two new runtime deps: `github.com/lestrrat-go/jwx/v2` (Go, MIT) and `react-oidc-context` + `oidc-client-ts` (SPA, Apache-2). Tracked in `go.mod` + `pnpm-lock.yaml`.
- SPA bundle grows ~80–100 KB for the CLOUD build.
- Integration tests require an LDZ-provisioned Dev User Pool → nightly-only cadence; PR-time CI stays hermetic.
- Memory-only token storage means silent-auth refresh on every SPA reload; acceptable UX trade for XSS mitigation, revisit only if cold-apply surfaces friction.

### Neutral

- `cognito:groups` / other claim axes remain additive future work per ADR-0022. No schema migration when they land — just wider claim extraction in `OIDCProvider`.
- Custom domain (`auth.staging.binhsu.org`) out of scope per #76 Path 1; Cognito-provided domain is fine for staging.

## Triggers to revisit

1. **Federation demand (Google / GitHub / SAML IdP)** — extend `OIDCProvider` to trust multiple issuers OR add a User Pool IdP bridge. Minor surgery, no ADR supersession.
2. **Move beyond Cognito** (Auth0 / Keycloak / Dex) — `auth.Provider` port abstracts IdP; `OIDCProvider` + SPA `<AuthProvider>` take different config, no interface change. Supersede only if library stack materially differs.
3. **Compliance regime requiring FIPS crypto at application layer** — Cognito KMS backend is FIPS-validated; aegis-core verifier uses stdlib `crypto/rsa` which honours FIPS mode via `GODEBUG=fips140=on` in Go 1.24+. Re-evaluate if stricter rules land (FedRAMP High / CJIS / HIPAA).
4. **Token lifetime tuning** — cold-apply UX may reveal 1h access-token pacing friction. Tune via Cognito app client config (no aegis-core change).
5. **Per-user RBAC becomes real** — add `cognito:groups` extraction in `OIDCProvider` + surface in `Principal`. Extension, not supersession.
6. **User count past Cognito free tier (50k MAU)** — Cognito pricing kicks in; re-evaluate providers. `auth.Provider` port makes the swap lightweight.

## Implementation checklist — new ROADMAP Phase 4e

To be added to `ROADMAP.md` under Phase 4 after this ADR is Accepted. Each slice ships with regression tests per Rule 2.

### 4e-1 — Gateway JWT middleware

- [ ] `gateway_go/internal/auth/oidc_provider.go` — `OIDCProvider` implementing `auth.Provider`
- [ ] `jwx/v2` dep added via `go get`; `go.mod` + `MODULE.bazel` + `gateway_go/BUILD.bazel` updated
- [ ] `cmd/gateway/main.go` factory — `DEPLOY_MODE` switch (`local` / `cloud` / `cloud-test`)
- [ ] Unit tests (`oidc_provider_test.go`) — mock JWKS httptest server + synthesised JWTs covering all failure modes
- [ ] Structured error logging (category, not token bytes)

### 4e-2 — SPA OAuth scaffold

- [ ] `frontend_web/package.json` — `react-oidc-context` + `oidc-client-ts` deps
- [ ] `frontend_web/src/main.tsx` — `<AuthProvider>` wrapper with Vite env-var config
- [ ] `frontend_web/src/routes/AuthCallback.tsx` — PKCE completion + navigate
- [ ] `frontend_web/src/lib/gateway-client.ts` — bearer injection on every outbound RPC
- [ ] `frontend_web/src/lib/auth.ts` — integrate with Cognito ID token OR augment with new `useAuth()` hook, preserving existing session-token storage for viewer-join flow
- [ ] Logout button → `user.signoutRedirect()`
- [ ] Vitest for AuthCallback + bearer injection

### 4e-3 — `custom:tenant_id` propagation verification

- [ ] Integration test: engine receives correct `tenant_id` + `sub` via gRPC metadata when gateway runs `OIDCProvider`
- [ ] Qdrant filter end-to-end: seed 2 tenants, verify cross-tenant isolation per ADR-0022
- [ ] Explicit failure-mode test: empty `custom:tenant_id` in JWT → gateway rejects with `Unauthenticated`

### 4e-4 — Integration + optional E2E

- [ ] Dev User Pool registration on LDZ staging Cognito (coord via #76 or new issue)
- [ ] Go integration test: real `AdminInitiateAuth` → token → `OIDCProvider.Authenticate` → expected `Principal`
- [ ] (Optional) Playwright nightly spec: SPA → Cognito Hosted UI → meeting create → hint render on staging

## Cost summary

- **Runtime overhead**: `jwk.Cache` ≈ 1KB per issuer; JWKS fetch every 15min sub-second; JWT verify is stdlib RSA (~100µs). SPA bundle +80–100KB for CLOUD build.
- **Dev ops**: one additional Dev User Pool on LDZ side (already in LDZ ADR-026 scope); no new LDZ asks from this ADR.
- **CI**: unit tests add <100ms at PR time. Integration test runs nightly.
- **Code**: ~300 LoC Go (provider + tests) + ~100 LoC TypeScript (provider wiring + callback route + bearer injection). One-time cost.
- **Future IdP migration**: `Provider` port stays; `OIDCProvider` config changes OR new `SomeOtherProvider` slots in. Swap is a config + dep change, not a rewrite.

## Open Questions

1. Does `frontend_web/src/lib/auth.ts` fully superseded by `useAuth()`, or kept for the session-token (viewer-join) path? Defer to 4e-2 implementation time after reading the module.
2. Does `gateway_go/internal/pipeline/` already forward `tenant_id` to the engine, or is that wiring LAN-only? Verify in 4e-3; if missing, scope adjusts by ~50 LoC.
3. Exactly which routes in the SPA should require authentication vs be public? (Host vs viewer — viewer joins via QR + session-token today, does viewer also need Cognito auth in CLOUD mode, or only host?) Needs a product-level decision before 4e-2 ships.
