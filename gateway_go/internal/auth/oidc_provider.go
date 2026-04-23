package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"google.golang.org/grpc/metadata"
)

// OIDCProvider validates Cognito-issued ID tokens against a live JWKS
// endpoint per ADR-0034 §D1. This is the CLOUD-mode production path;
// StaticJWTProvider (HS256 shared secret) remains for integration-test
// scenarios that need pre-shared-secret tokens (DEPLOY_MODE=cloud-test).
//
// Mechanism:
//
//   - RS256 signatures verified against a live JWKS set, selected by
//     the token's `kid` header
//   - JWKS cache refreshed in-process every 15 minutes by default
//     (Cognito rotates signing keys on the order of months; 15min is
//     conservative)
//   - Issuer, audience, and expiration validated mandatorily
//   - Missing `custom:tenant_id` rejects the request — no silent
//     fallback to empty TenantID, which would conflate with
//     LOCAL-mode semantics
//
// Claim → Principal mapping (per ADR-0034 §D1 + ADR-0022 §Schema
// shape):
//
//	sub              → Principal.UserID
//	custom:tenant_id → Principal.TenantID
//
// Construct via NewOIDCProvider; a zero OIDCProvider is invalid.
type OIDCProvider struct {
	issuer   string
	audience string
	cache    *jwk.Cache
	jwksURL  string

	// Overridable claim names. Production defaults are
	// "sub" / "custom:tenant_id"; tests occasionally override to
	// exercise the claim-missing branches on synthetic schemas.
	userIDClaim   string
	tenantIDClaim string
}

// OIDCConfig configures a new OIDCProvider.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL. For Cognito:
	// "https://cognito-idp.<region>.amazonaws.com/<pool-id>".
	// Must match the token's "iss" claim exactly.
	Issuer string

	// Audience is the Cognito app client ID; matches the "aud" claim
	// exactly. Cognito ID tokens carry a single-value audience.
	Audience string

	// JWKSURL overrides the derived JWKS location. Default:
	// Issuer + "/.well-known/jwks.json" (Cognito convention).
	// Overridden in tests to point at an httptest server.
	JWKSURL string

	// RefreshInterval overrides the JWKS cache refresh cadence.
	// Default 15 minutes. Lower values in tests are fine; jwk.Cache
	// treats this as a minimum, not a maximum.
	RefreshInterval time.Duration

	// UserIDClaim overrides the "sub" default. Rare — exposed for
	// tests that verify the missing-claim branch against a synthetic
	// token shape.
	UserIDClaim string

	// TenantIDClaim overrides the "custom:tenant_id" default. Same
	// rationale as UserIDClaim.
	TenantIDClaim string
}

// defaultJWKSRefreshInterval matches ADR-0034 §D1 "JWKS refresh on a
// 15-minute ticker". Conservative vs. Cognito's months-scale rotation.
const defaultJWKSRefreshInterval = 15 * time.Minute

// NewOIDCProvider builds a provider and pre-warms the JWKS cache.
// Pre-warming is load-bearing: if the Cognito JWKS endpoint is
// unreachable at construction, we want to fail the gateway's startup
// sequence, not silently degrade to "reject every request because the
// cache is empty". The ctx parameter scopes the cache's internal
// refresh goroutines — typically main.Context() so the cache lives
// for the gateway's lifetime.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("OIDCProvider: Issuer is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("OIDCProvider: Audience is required")
	}

	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		jwksURL = strings.TrimRight(cfg.Issuer, "/") + "/.well-known/jwks.json"
	}
	refresh := cfg.RefreshInterval
	if refresh <= 0 {
		refresh = defaultJWKSRefreshInterval
	}

	cache := jwk.NewCache(ctx)
	if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(refresh)); err != nil {
		return nil, fmt.Errorf("OIDCProvider: register JWKS %s: %w", jwksURL, err)
	}
	if _, err := cache.Refresh(ctx, jwksURL); err != nil {
		return nil, fmt.Errorf("OIDCProvider: initial JWKS fetch %s: %w", jwksURL, err)
	}

	userID := cfg.UserIDClaim
	if userID == "" {
		userID = defaultUserIDClaim
	}
	tenantID := cfg.TenantIDClaim
	if tenantID == "" {
		tenantID = defaultTenantIDClaim
	}

	return &OIDCProvider{
		issuer:        cfg.Issuer,
		audience:      cfg.Audience,
		cache:         cache,
		jwksURL:       jwksURL,
		userIDClaim:   userID,
		tenantIDClaim: tenantID,
	}, nil
}

// Authenticate reads the Bearer token from gRPC metadata, verifies
// its RS256 signature against a JWKS key identified by the token's
// `kid` header, validates iss + aud + exp, and maps the sub +
// custom:tenant_id claims into a Principal.
//
// Error strings follow the StaticJWTProvider convention: generic
// shapes ("invalid token", "missing X claim") so a probing attacker
// cannot distinguish "signature mismatch" from "audience mismatch".
// The interceptor collapses everything into codes.Unauthenticated
// on the wire; structured logs on the server side retain the
// wrapped error for operational debugging.
func (p *OIDCProvider) Authenticate(ctx context.Context) (Principal, error) {
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
	tokenBytes := []byte(strings.TrimPrefix(raw, bearerPrefix))

	set, err := p.cache.Get(ctx, p.jwksURL)
	if err != nil {
		// JWKS fetch failure is distinct from a bad token — it's an
		// operational issue (network partition, Cognito outage). The
		// wire-level shape is still Unauthenticated, but the error
		// string lets the server-side log distinguish "our JWKS is
		// down" from "token was bad".
		return Principal{}, fmt.Errorf("JWKS unavailable: %w", err)
	}

	token, err := jwt.Parse(
		tokenBytes,
		jwt.WithKeySet(set),
		jwt.WithIssuer(p.issuer),
		jwt.WithAudience(p.audience),
		jwt.WithValidate(true),
	)
	if err != nil {
		return Principal{}, errors.New("invalid token")
	}

	userID, ok := getStringClaim(token, p.userIDClaim)
	if !ok || userID == "" {
		return Principal{}, errors.New("missing user id claim")
	}
	tenantID, ok := getStringClaim(token, p.tenantIDClaim)
	if !ok || tenantID == "" {
		return Principal{}, errors.New("missing tenant id claim")
	}

	return Principal{
		UserID:   userID,
		TenantID: tenantID,
		Mode:     ModeCloud,
	}, nil
}

// getStringClaim pulls a named claim out of a parsed token as a
// string. Standard claims (sub) have dedicated accessors on
// jwt.Token; custom claims go through Get which returns an
// interface{} we must type-assert.
func getStringClaim(token jwt.Token, name string) (string, bool) {
	if name == defaultUserIDClaim {
		sub := token.Subject()
		return sub, sub != ""
	}
	v, ok := token.Get(name)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}
