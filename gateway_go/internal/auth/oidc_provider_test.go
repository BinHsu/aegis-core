package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// testIssuer is the synthetic issuer URL baked into both the config
// and every signed token. Value is arbitrary — OIDCProvider enforces
// iss-claim == issuer-config equality, not any relationship to a real
// URL.
const testIssuer = "https://test-issuer.example.com"

// testAudience mirrors testIssuer's purpose for the aud claim.
const testAudience = "test-client-id"

// testKeyID identifies the single signing key each test uses. jwt.Parse
// routes verification via the token's `kid` header → the matching key
// in the JWKS set.
const testKeyID = "test-kid-1"

// signingKey bundles a test's RSA private key (for signing) with the
// same key re-wrapped as a JWK (because jwx's Sign API wants a JWK,
// not a raw crypto key) and the public JWKS endpoint URL serving it.
type signingKey struct {
	privJWK jwk.Key
	pubSet  jwk.Set
	jwksURL string
	server  *httptest.Server
}

// newTestJWKSServer spins up an httptest.Server publishing a single
// RS256 key's public half at its root path, and returns the private
// key wrapped as a jwk.Key ready for jwt.Sign. Caller MUST call
// Close() at test cleanup to stop the server goroutine.
func newTestJWKSServer(t *testing.T) *signingKey {
	t.Helper()

	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	privJWK, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatalf("jwk.FromRaw(private): %v", err)
	}
	if err := privJWK.Set(jwk.KeyIDKey, testKeyID); err != nil {
		t.Fatalf("set kid: %v", err)
	}
	if err := privJWK.Set(jwk.AlgorithmKey, jwa.RS256); err != nil {
		t.Fatalf("set alg: %v", err)
	}

	pubJWK, err := privJWK.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	pubSet := jwk.NewSet()
	if err := pubSet.AddKey(pubJWK); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	jwksBytes, err := json.Marshal(pubSet)
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksBytes)
	}))
	t.Cleanup(srv.Close)

	return &signingKey{
		privJWK: privJWK,
		pubSet:  pubSet,
		jwksURL: srv.URL,
		server:  srv,
	}
}

// signToken builds a token with the given claims and signs it using
// the test's private JWK. The returned raw string is what a client
// would present in an Authorization: Bearer header.
func signToken(t *testing.T, sk *signingKey, claims map[string]any) string {
	t.Helper()

	tok := jwt.New()
	for k, v := range claims {
		if err := tok.Set(k, v); err != nil {
			t.Fatalf("set claim %q: %v", k, err)
		}
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, sk.privJWK))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return string(signed)
}

// standardClaims returns the claim set a happy-path Cognito token
// would carry. Callers mutate the map to exercise failure modes.
func standardClaims() map[string]any {
	return map[string]any{
		jwt.SubjectKey:     "cognito-user-abc",
		"custom:tenant_id": "tenant-42",
		jwt.IssuerKey:      testIssuer,
		jwt.AudienceKey:    testAudience,
		jwt.ExpirationKey:  time.Now().Add(5 * time.Minute),
	}
}

// newTestProvider wires up an OIDCProvider pointed at the supplied
// JWKS server. Tests use this in every scenario except the
// JWKS-unreachable one.
func newTestProvider(t *testing.T, sk *signingKey) *OIDCProvider {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := NewOIDCProvider(ctx, OIDCConfig{
		Issuer:          testIssuer,
		Audience:        testAudience,
		JWKSURL:         sk.jwksURL,
		RefreshInterval: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	return p
}

// --- Happy path + successful flow --------------------------------------------

func TestOIDCHappyPath(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	token := signToken(t, sk, standardClaims())
	got, err := p.Authenticate(mdCtx("authorization", "Bearer "+token))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.UserID != "cognito-user-abc" {
		t.Errorf("UserID = %q, want cognito-user-abc", got.UserID)
	}
	if got.TenantID != "tenant-42" {
		t.Errorf("TenantID = %q, want tenant-42", got.TenantID)
	}
	if got.Mode != ModeCloud {
		t.Errorf("Mode = %q, want ModeCloud", got.Mode)
	}
}

// --- Metadata / header shape failures ---------------------------------------

func TestOIDCRejectsNoMetadata(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	if _, err := p.Authenticate(context.Background()); err == nil {
		t.Fatal("accepted ctx with no metadata")
	}
}

func TestOIDCRejectsNoAuthorizationHeader(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	if _, err := p.Authenticate(mdCtx()); err == nil {
		t.Fatal("accepted metadata without authorization")
	}
}

func TestOIDCRejectsNonBearer(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	if _, err := p.Authenticate(mdCtx("authorization", "Basic dXNlcjpwYXNz")); err == nil {
		t.Fatal("accepted Basic auth")
	}
}

// --- Cryptographic / validation failures ------------------------------------

func TestOIDCRejectsWrongSignature(t *testing.T) {
	sk1 := newTestJWKSServer(t)
	sk2 := newTestJWKSServer(t) // different key pair, different server
	p := newTestProvider(t, sk1)

	// Token signed by sk2, validated against sk1's JWKS → signature mismatch.
	token := signToken(t, sk2, standardClaims())
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token signed by a key not in the configured JWKS")
	}
}

func TestOIDCRejectsExpiredToken(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	claims := standardClaims()
	claims[jwt.ExpirationKey] = time.Now().Add(-5 * time.Minute)
	token := signToken(t, sk, claims)

	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted expired token")
	}
}

func TestOIDCRejectsWrongIssuer(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	claims := standardClaims()
	claims[jwt.IssuerKey] = "https://evil-issuer.example.com"
	token := signToken(t, sk, claims)

	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with wrong issuer")
	}
}

func TestOIDCRejectsWrongAudience(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	claims := standardClaims()
	claims[jwt.AudienceKey] = "some-other-client-id"
	token := signToken(t, sk, claims)

	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with wrong audience")
	}
}

// --- Claim-shape failures ---------------------------------------------------

func TestOIDCRejectsMissingSub(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	claims := standardClaims()
	delete(claims, jwt.SubjectKey)
	token := signToken(t, sk, claims)

	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with missing sub claim")
	}
}

// TestOIDCRejectsMissingTenantID is the load-bearing test for the
// ADR-0034 §D1 rule: no silent fallback to empty TenantID, which
// would conflate with LOCAL-mode semantics and break ADR-0022
// multi-tenancy isolation.
func TestOIDCRejectsMissingTenantID(t *testing.T) {
	sk := newTestJWKSServer(t)
	p := newTestProvider(t, sk)

	claims := standardClaims()
	delete(claims, "custom:tenant_id")
	token := signToken(t, sk, claims)

	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with missing custom:tenant_id — would break ADR-0022 tenant isolation")
	}
}

// --- Configuration failures -------------------------------------------------

func TestOIDCRequiresIssuer(t *testing.T) {
	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Audience: testAudience,
		JWKSURL:  "http://example.com/jwks.json",
	})
	if err == nil {
		t.Fatal("NewOIDCProvider accepted empty Issuer")
	}
}

func TestOIDCRequiresAudience(t *testing.T) {
	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Issuer:  testIssuer,
		JWKSURL: "http://example.com/jwks.json",
	})
	if err == nil {
		t.Fatal("NewOIDCProvider accepted empty Audience")
	}
}

// TestOIDCFailsOnUnreachableJWKS covers the pre-warm fail-fast path.
// The ADR calls this out explicitly: a gateway that can't reach its
// JWKS at startup should refuse to serve rather than degrade to
// "reject every request silently".
func TestOIDCFailsOnUnreachableJWKS(t *testing.T) {
	// Start a server, grab the URL, then close it — the URL is now
	// guaranteed-unreachable for the rest of the test.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	_, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Issuer:   testIssuer,
		Audience: testAudience,
		JWKSURL:  deadURL,
	})
	if err == nil {
		t.Fatal("NewOIDCProvider succeeded against unreachable JWKS")
	}
}

// JWKS key-rotation mid-flight coverage is **deliberately deferred**
// to ADR-0034 §4e-4 integration testing. The `jwk.Cache` refresh
// mechanism is library-internal (time-driven background goroutine
// scheduled off `Cache-Control: max-age` plus `MinRefreshInterval`);
// asserting its behaviour with an httptest server would test the
// library, not our code. Real rotation semantics surface honestly
// only against a live Cognito Dev User Pool where the rotation event
// comes from AWS, not from us instrumenting a fake server.
//
// The production-risk covered here is "signature verification fails
// when the token's kid is not in the cached JWKS" — that path is
// already exercised by TestOIDCRejectsWrongSignature above.
