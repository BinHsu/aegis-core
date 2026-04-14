// Package auth defines the Gateway's authentication port (per
// ARCH §5 Hexagonal Architecture) and the providers that implement
// it across the Local-mode / Cloud-mode dual deployment topology.
//
// Wire shape:
//
//	gRPC request  →  UnaryInterceptor  →  Provider.Authenticate  →  Principal
//	                                                                ↓
//	                                                    ctx = WithPrincipal(ctx, p)
//	                                                                ↓
//	                                                           handler(ctx, req)
//
// Handlers that care about the caller's identity read it back via
// FromContext; handlers that don't care ignore it. Only two handlers
// currently care (CreateMeeting for tenant tagging, EndMeeting for the
// host-only policy check planned for A5 follow-up) — everything else
// is a passthrough.
//
// The Provider interface is the only surface this package exports for
// dependency injection. cmd/gateway/main.go chooses one at startup
// (Local ⇒ NoOpProvider, Cloud ⇒ StaticJWTProvider or a future real
// Cognito client) and passes it through
// grpc.UnaryInterceptor / grpc.StreamInterceptor.
package auth

import "context"

// Mode is a small, stable tag for which deployment flavor produced the
// Principal. Cheap to branch on when a handler wants different behavior
// between the two modes — e.g. tenant isolation checks apply only in
// Cloud mode where multiple tenants share one Gateway replica.
type Mode string

const (
	// ModeLocal indicates the request came from Local mode where no
	// tenant identity exists; the Principal is synthetic and uniform.
	ModeLocal Mode = "local"
	// ModeCloud indicates the request carried a validated JWT whose
	// claims were mapped into the Principal.
	ModeCloud Mode = "cloud"
)

// Principal is the authenticated caller identity propagated through
// the request context. Fields are deliberately sparse — this is the
// minimal identity surface the Gateway needs; per-handler authorization
// (e.g. "only the host can end the meeting") layers on top.
type Principal struct {
	// UserID is the stable per-user identifier. In Cloud mode this is
	// the Cognito `sub` claim. In Local mode it is the synthetic
	// string "local".
	UserID string
	// TenantID is the tenant / organization identifier that scopes
	// session visibility. Empty in Local mode (ADR-0007 L7); populated
	// from the Cognito `custom:tenant_id` claim in Cloud mode.
	TenantID string
	// Mode is a small tag for deployment-flavor branching.
	Mode Mode
}

// Provider is the Hexagonal Architecture port that
// cmd/gateway/main.go injects at startup time. Implementations pull
// whatever they need out of the request context (typically gRPC
// metadata or the request headers on the WebSocket upgrade path).
//
// Returning a non-nil error causes the interceptor to reject the
// request with gRPC codes.Unauthenticated. A successful return MUST
// produce a Principal whose UserID is non-empty — an empty UserID
// would make downstream per-user rate limits / audit logs ambiguous.
type Provider interface {
	Authenticate(ctx context.Context) (Principal, error)
}

// principalKey is the package-private context key type. A struct{} is
// the idiomatic form for context keys — zero-sized, no allocation,
// and impossible to collide with keys from other packages.
type principalKey struct{}

// WithPrincipal returns ctx annotated with p. Exported because the
// WebSocket viewer handler (internal/ws) reuses the same Principal
// shape on its own authentication path and needs to set it explicitly.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext extracts the Principal an upstream interceptor attached.
// The second return is false if no interceptor ran — typically only
// possible in unit tests where the handler is invoked directly;
// production code paths always run through the interceptor chain.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
