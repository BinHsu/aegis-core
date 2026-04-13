// Package token implements the short-lived JWT join tokens described in
// ADR-0001 Option B. The invariants that matter for security review:
//
//  1. The signing key is **process-scoped**: generated at startup from
//     crypto/rand, never persisted, never logged. When the Gateway
//     restarts, all previously issued tokens become invalid. This is
//     intentional — per ADR-0004 the server holds no meeting content,
//     so restart terminates any in-flight session anyway
//     (ARCHITECTURE.md §11 L2).
//
//  2. The token binds the session_id into the subject claim. Verify
//     always takes the session_id the caller *expects* and checks
//     that the token is bound to it — a leaked token cannot be
//     replayed against a different session.
//
//  3. HS256 is the only accepted algorithm. The explicit WithValidMethods
//     option prevents the `alg: none` attack and algorithm confusion
//     attacks where a token forged with HS256 is verified as RS256
//     using the public key as the HMAC secret.
//
//  4. The library treats exp as required; a missing or nil exp claim
//     is rejected.
//
// Phase 5+ migration path (when ADR-0001 Option A's allowlist lands):
// add an additional claim `aud` listing allowed account ids, and
// layer a second middleware that validates the incoming Cognito JWT's
// sub against that list. Existing single-claim tokens continue to
// verify unchanged when no allowlist is configured.
package token

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidToken is returned by Verify when the token is malformed,
// expired, signed with the wrong key, bound to a different session,
// or otherwise not acceptable. The gRPC layer translates this to
// codes.PermissionDenied (per proto/aegis/v1/aegis.proto JoinAsViewer
// error table).
var ErrInvalidToken = errors.New("token: invalid")

// Issuer signs and verifies viewer join tokens for one Gateway replica.
//
// An Issuer is safe for concurrent use after construction. All state
// (the signing key) is immutable for the lifetime of the process.
type Issuer struct {
	key []byte
	now func() time.Time // test seam
}

// NewIssuer generates a fresh 32-byte HS256 key from crypto/rand.
// The key lives only in process memory.
//
// If the OS RNG fails (essentially impossible on a functioning Linux/
// macOS kernel), this returns an error rather than panic so the
// caller can decide whether to fail startup.
func NewIssuer() (*Issuer, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("token: generate signing key: %w", err)
	}
	return &Issuer{key: key, now: time.Now}, nil
}

// newIssuerWithKey is an internal constructor used by tests and by
// NewIssuer. It exists as a seam so tests can inject a deterministic
// clock without reaching into unexported fields.
func newIssuerWithKey(key []byte, now func() time.Time) *Issuer {
	return &Issuer{key: key, now: now}
}

// Claims is the exact set of claims Aegis tokens carry. Keeping this
// struct explicit (rather than jwt.MapClaims) ensures we fail loudly
// if a verifier encounters an unexpected claim shape.
type Claims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Issue signs a token bound to sessionID with the given expiry.
//
// The caller is responsible for choosing `exp` according to
// ADR-0001 defaults (session_max_lifetime + 10m grace).
func (i *Issuer) Issue(sessionID string, exp time.Time) (string, error) {
	if sessionID == "" {
		return "", errors.New("token: sessionID must not be empty")
	}
	if exp.IsZero() {
		return "", errors.New("token: exp must be non-zero")
	}
	now := i.now()
	claims := Claims{
		SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "aegis-gateway",
			Subject:   sessionID,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := t.SignedString(i.key)
	if err != nil {
		return "", fmt.Errorf("token: sign: %w", err)
	}
	return signed, nil
}

// Verify parses and validates a token, returning ErrInvalidToken on
// any failure. It enforces:
//
//   - HS256 signing algorithm (prevents alg:none and alg confusion).
//   - Signature matches this Issuer's process-scoped key.
//   - `exp` claim is present and in the future.
//   - `sub` claim (and the custom `sid` claim) equal expectedSessionID.
//
// The expectedSessionID argument is load-bearing: callers MUST pass
// the session id they got from the URL path / request, not parse it
// out of the token. Otherwise a token bound to session A could be
// replayed against session B.
func (i *Issuer) Verify(raw, expectedSessionID string) (*Claims, error) {
	if raw == "" {
		return nil, ErrInvalidToken
	}
	if expectedSessionID == "" {
		return nil, errors.New("token: expectedSessionID must not be empty")
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(i.now),
	)

	claims := &Claims{}
	tok, err := parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
		// Belt-and-suspenders: WithValidMethods already rejects other
		// algs, but we explicitly re-check the concrete type here so
		// that a future library refactor doesn't silently open the
		// alg-confusion hole.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return i.key, nil
	})
	if err != nil || !tok.Valid {
		return nil, ErrInvalidToken
	}
	if claims.SessionID != expectedSessionID || claims.Subject != expectedSessionID {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
