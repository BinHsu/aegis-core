package token

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fixedClock returns a closure that always reports fixedNow. Tests
// that need to advance time wrap a pointer instead.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newTestIssuer(t *testing.T, now time.Time) *Issuer {
	t.Helper()
	// Deterministic key — real code uses crypto/rand but the
	// tests want reproducible failures when something regresses.
	key := []byte("0123456789abcdef0123456789abcdef")
	return newIssuerWithKey(key, fixedClock(now))
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	sid := "abc123xyz"
	exp := now.Add(4 * time.Hour)
	raw, err := iss.Issue(sid, exp)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(raw, ".") {
		t.Fatalf("expected JWT format with '.' separators, got %q", raw)
	}

	claims, err := iss.Verify(raw, sid)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.SessionID != sid {
		t.Fatalf("SessionID: got %q, want %q", claims.SessionID, sid)
	}
	if claims.Subject != sid {
		t.Fatalf("Subject: got %q, want %q", claims.Subject, sid)
	}
	if !claims.ExpiresAt.Time.Equal(exp) {
		t.Fatalf("ExpiresAt: got %v, want %v", claims.ExpiresAt.Time, exp)
	}
}

func TestVerifyRejectsWrongSession(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	raw, err := iss.Issue("session-A", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The attack: a token bound to session-A is presented against
	// session-B. Verify MUST reject.
	if _, err := iss.Verify(raw, "session-B"); err != ErrInvalidToken {
		t.Fatalf("cross-session verify: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	raw, err := iss.Issue("s1", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Fast-forward 2 minutes — the token is now past its exp.
	futureIss := newIssuerWithKey(iss.key, fixedClock(now.Add(2*time.Minute)))
	if _, err := futureIss.Verify(raw, "s1"); err != ErrInvalidToken {
		t.Fatalf("expired verify: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss1 := newIssuerWithKey([]byte("key-one-xxxxxxxxxxxxxxxxxxxxxxxx"), fixedClock(now))
	iss2 := newIssuerWithKey([]byte("key-two-yyyyyyyyyyyyyyyyyyyyyyyy"), fixedClock(now))

	raw, err := iss1.Issue("s1", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := iss2.Verify(raw, "s1"); err != ErrInvalidToken {
		t.Fatalf("wrong-key verify: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	// Hand-craft a token with alg=none. This is the classic
	// downgrade attack — WithValidMethods must reject.
	claims := &Claims{
		SessionID: "s1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "s1",
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("crafting alg=none token: %v", err)
	}
	if _, err := iss.Verify(raw, "s1"); err != ErrInvalidToken {
		t.Fatalf("alg=none verify: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsMissingExp(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	// Craft a valid-HS256 token that omits exp.
	claims := &Claims{
		SessionID: "s1",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "s1",
		},
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(iss.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := iss.Verify(raw, "s1"); err != ErrInvalidToken {
		t.Fatalf("missing-exp verify: got %v, want ErrInvalidToken", err)
	}
}

func TestVerifyRejectsEmptyToken(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	if _, err := iss.Verify("", "s1"); err != ErrInvalidToken {
		t.Fatalf("empty token: got %v, want ErrInvalidToken", err)
	}
	if _, err := iss.Verify("not.a.jwt", "s1"); err != ErrInvalidToken {
		t.Fatalf("garbage token: got %v, want ErrInvalidToken", err)
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	now := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	iss := newTestIssuer(t, now)

	if _, err := iss.Issue("", now.Add(time.Hour)); err == nil {
		t.Fatalf("empty sid: want error, got nil")
	}
	if _, err := iss.Issue("s1", time.Time{}); err == nil {
		t.Fatalf("zero exp: want error, got nil")
	}
}

func TestNewIssuerProducesUniqueKeys(t *testing.T) {
	a, err := NewIssuer()
	if err != nil {
		t.Fatalf("NewIssuer a: %v", err)
	}
	b, err := NewIssuer()
	if err != nil {
		t.Fatalf("NewIssuer b: %v", err)
	}
	if string(a.key) == string(b.key) {
		t.Fatalf("two Issuers produced identical keys — crypto/rand regression?")
	}
	if len(a.key) != 32 {
		t.Fatalf("key length: got %d, want 32", len(a.key))
	}
}
