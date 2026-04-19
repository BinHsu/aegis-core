// Package cors implements the gateway's CORS / origin allowlist policy.
//
// Today (ADR-0007 Local mode) the gateway is permissive — accepts any
// Origin — because the LAN viewer flow scans QR codes from arbitrary
// browsers on phones whose hostnames the host has no way to know in
// advance. That is correct for Local mode.
//
// Cloud mode (Phase 4a-5 + onward) requires tightening because the
// gateway sits at `aegis-api.staging.binhsu.org` and the frontend at
// `aegis-app.staging.binhsu.org` (split subdomain per ADR-0027). Without an
// allowlist any origin can hit gRPC-Web from a browser — including a
// hostile origin that tricks a logged-in user into making cross-origin
// requests with their session cookie / Bearer token.
//
// Policy contract:
//   - Empty AEGIS_ALLOWED_ORIGINS (default) → permissive, behaviour
//     unchanged from pre-Slice-5 (Local mode preserves).
//   - Non-empty AEGIS_ALLOWED_ORIGINS (comma-separated, e.g.
//     "https://aegis-app.staging.binhsu.org,https://aegis-app.prod.binhsu.org") →
//     strict allowlist. Only listed origins pass.
//
// The Policy type is intentionally side-effect free at construction
// time (reads env once, then immutable). Callers build the policy in
// main and pass it into both the grpcweb wrapper's WithOriginFunc and
// the CORS-header-writing handler.
package cors

import (
	"os"
	"strings"
)

// EnvVar is the env var name the policy reads at construction.
const EnvVar = "AEGIS_ALLOWED_ORIGINS"

// Policy carries the parsed allowlist + a flag distinguishing the
// two operational modes.
type Policy struct {
	permissive bool
	allowlist  map[string]struct{}
}

// New constructs a Policy from the AEGIS_ALLOWED_ORIGINS env var.
// Empty / unset → permissive policy. Non-empty → strict allowlist.
// Whitespace around comma-separated entries is trimmed; empty
// entries between commas are ignored.
func New() *Policy {
	raw := strings.TrimSpace(os.Getenv(EnvVar))
	if raw == "" {
		return &Policy{permissive: true}
	}
	set := map[string]struct{}{}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			set[p] = struct{}{}
		}
	}
	if len(set) == 0 {
		// Env was set but contained only whitespace / commas — treat as
		// permissive rather than as a strict-but-empty allowlist that
		// would refuse every origin and likely surprise the operator.
		return &Policy{permissive: true}
	}
	return &Policy{allowlist: set}
}

// Allow returns true iff the given origin is permitted by this policy.
// In permissive mode, every origin (including empty / non-browser
// requests) returns true. In strict mode, only exact-match listed
// origins return true.
func (p *Policy) Allow(origin string) bool {
	if p.permissive {
		return true
	}
	_, ok := p.allowlist[origin]
	return ok
}

// Permissive reports whether the policy is in the wildcard-Local-mode
// posture (true) vs the strict-allowlist posture (false). Useful for
// the CORS-header writer to decide between echo-origin (strict) and
// Access-Control-Allow-Origin: * (permissive).
func (p *Policy) Permissive() bool {
	return p.permissive
}
