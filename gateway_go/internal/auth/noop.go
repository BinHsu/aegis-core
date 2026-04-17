package auth

import "context"

// NoOpProvider is the Local-mode implementation of Provider. It never
// rejects a request and always produces the same synthetic Principal,
// because ADR-0007 Local mode is explicitly single-tenant single-user
// — there is no identity provider and no tenant concept.
//
// The synthetic Principal is load-bearing even though it's uniform:
// downstream handlers still see a well-formed Principal in ctx, so
// the "did auth run?" branch never needs to special-case Local mode.
// Uniformity of the request shape between Local and Cloud is the
// whole point of this port.
type NoOpProvider struct{}

// localPrincipal is the single value every NoOp call returns. Declared
// as a package var (not a literal in Authenticate) so the returned
// Principal is pointer-equal across calls — tests can use == to verify
// "the same Principal made it through the interceptor".
var localPrincipal = Principal{
	UserID:   "local",
	TenantID: "",
	Mode:     ModeLocal,
}

// Authenticate returns the static local Principal unconditionally.
// The ctx parameter is accepted for interface conformance; no data
// is read from it.
func (NoOpProvider) Authenticate(_ context.Context) (Principal, error) {
	return localPrincipal, nil
}
