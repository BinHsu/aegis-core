package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc/metadata"
)

// StaticJWTProvider validates bearer tokens presented as an
// `authorization` gRPC metadata header. It is a DEVELOPMENT /
// integration-test STUB — NOT a production Cognito client.
//
// See Phase 2 "Known Gaps" in ROADMAP.md for the scope of what a real
// Cognito integration needs (live JWKS fetching from the User Pool
// URL, key-ID (kid) header routing, periodic re-fetch, Cognito claim
// mapping). This stub exists so Cloud-mode code paths can be
// exercised end-to-end in tests without spinning up AWS.
//
// Signing method: HS256 (shared secret). Real Cognito uses RS256 and
// publishes a JWKS endpoint; the switch from HS256 here to a real
// JWKS-backed RS256 validator is a drop-in replacement of the
// Keyfunc closure inside Authenticate — no callers (interceptor,
// main.go wiring) change.
type StaticJWTProvider struct {
	// Secret is the HMAC key. Must be non-empty; keeping it as a
	// pointer/slice field (not a string) so zeroing it before
	// program exit is possible if the deploy surface ever requires
	// that level of hygiene (it doesn't today).
	Secret []byte
	// ExpectedAudience, when non-empty, is enforced during
	// validation. Matches the JWT `aud` claim exactly (no prefix or
	// pattern support — Cognito always issues a single-value aud).
	ExpectedAudience string
	// UserIDClaim names the claim holding the user identifier.
	// Default "sub" (Cognito convention).
	UserIDClaim string
	// TenantIDClaim names the claim holding the tenant identifier.
	// Default "custom:tenant_id" (our convention; Cognito custom
	// claims use the "custom:" prefix).
	TenantIDClaim string
}

const (
	defaultUserIDClaim   = "sub"
	defaultTenantIDClaim = "custom:tenant_id"
	bearerPrefix         = "Bearer "
)

// Authenticate reads the `authorization` metadata header, parses the
// Bearer token as an HS256 JWT, validates the signature against
// p.Secret, and returns a Principal populated from the claims.
//
// Errors are deliberately unspecific ("invalid token", "expired",
// etc.) so an attacker probing the endpoint cannot distinguish
// between "I used the wrong key" and "my key was right but my claim
// set was wrong" — both return the same shape. The interceptor
// further collapses this into a generic Unauthenticated code on the
// wire.
func (p StaticJWTProvider) Authenticate(ctx context.Context) (Principal, error) {
	if len(p.Secret) == 0 {
		return Principal{}, errors.New("StaticJWTProvider: no secret configured")
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return Principal{}, errors.New("missing metadata")
	}
	auths := md.Get("authorization")
	if len(auths) == 0 {
		return Principal{}, errors.New("missing authorization header")
	}
	raw := auths[0]
	if !strings.HasPrefix(raw, bearerPrefix) {
		return Principal{}, errors.New("authorization is not a Bearer token")
	}
	raw = strings.TrimPrefix(raw, bearerPrefix)

	// Parse + validate in one call. The Keyfunc receives the parsed
	// (unvalidated) header and returns the key to verify the
	// signature; restricting Method to *SigningMethodHMAC prevents
	// the "alg=none" attack class — a real Cognito client would
	// restrict to *SigningMethodRSA instead.
	claims := jwt.MapClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %q", t.Header["alg"])
		}
		return p.Secret, nil
	})
	if err != nil || !tok.Valid {
		return Principal{}, errors.New("invalid token")
	}

	if p.ExpectedAudience != "" {
		audClaim, err := claims.GetAudience()
		if err != nil {
			return Principal{}, errors.New("invalid audience")
		}
		matched := false
		for _, a := range audClaim {
			if a == p.ExpectedAudience {
				matched = true
				break
			}
		}
		if !matched {
			return Principal{}, errors.New("invalid audience")
		}
	}

	userIDClaim := p.UserIDClaim
	if userIDClaim == "" {
		userIDClaim = defaultUserIDClaim
	}
	tenantIDClaim := p.TenantIDClaim
	if tenantIDClaim == "" {
		tenantIDClaim = defaultTenantIDClaim
	}
	userID, _ := claims[userIDClaim].(string)
	if userID == "" {
		return Principal{}, errors.New("missing user id claim")
	}
	tenantID, _ := claims[tenantIDClaim].(string)

	return Principal{
		UserID:   userID,
		TenantID: tenantID,
		Mode:     ModeCloud,
	}, nil
}
