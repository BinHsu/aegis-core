package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// --- NoOpProvider ------------------------------------------------------------

func TestNoOpAlwaysReturnsLocalPrincipal(t *testing.T) {
	p, err := NoOpProvider{}.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.UserID != "local" {
		t.Errorf("UserID = %q, want %q", p.UserID, "local")
	}
	if p.TenantID != "" {
		t.Errorf("TenantID = %q, want empty", p.TenantID)
	}
	if p.Mode != ModeLocal {
		t.Errorf("Mode = %q, want %q", p.Mode, ModeLocal)
	}
}

// --- Context helpers ---------------------------------------------------------

func TestWithPrincipalAndFromContext(t *testing.T) {
	base := context.Background()
	if _, ok := FromContext(base); ok {
		t.Fatal("FromContext returned ok on a clean ctx")
	}

	p := Principal{UserID: "u1", TenantID: "t1", Mode: ModeCloud}
	ctx := WithPrincipal(base, p)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext did not recover Principal")
	}
	if got != p {
		t.Errorf("got %+v, want %+v", got, p)
	}
}

// --- UnaryInterceptor --------------------------------------------------------

// recordingProvider lets tests assert which Principal the interceptor
// produced AND whether the provider was called.
type recordingProvider struct {
	principal Principal
	err       error
	calls     int
}

func (r *recordingProvider) Authenticate(_ context.Context) (Principal, error) {
	r.calls++
	return r.principal, r.err
}

func TestUnaryInterceptorPassesPrincipalToHandler(t *testing.T) {
	p := Principal{UserID: "alice", TenantID: "acme", Mode: ModeCloud}
	rp := &recordingProvider{principal: p}
	interceptor := UnaryInterceptor(rp)

	var gotPrincipal Principal
	var gotOK bool
	handler := func(ctx context.Context, _ any) (any, error) {
		gotPrincipal, gotOK = FromContext(ctx)
		return "ok", nil
	}
	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want ok", resp)
	}
	if !gotOK {
		t.Fatal("handler did not see a Principal")
	}
	if gotPrincipal != p {
		t.Errorf("handler saw %+v, want %+v", gotPrincipal, p)
	}
	if rp.calls != 1 {
		t.Errorf("Authenticate called %d times, want 1", rp.calls)
	}
}

func TestUnaryInterceptorMapsErrorToUnauthenticated(t *testing.T) {
	rp := &recordingProvider{err: errors.New("nope")}
	interceptor := UnaryInterceptor(rp)

	handlerCalled := false
	handler := func(_ context.Context, _ any) (any, error) {
		handlerCalled = true
		return nil, nil
	}
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err == nil {
		t.Fatal("interceptor returned nil error on failing provider")
	}
	if handlerCalled {
		t.Error("handler was invoked despite auth failure")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("status code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestUnaryInterceptorPanicsOnNilProvider(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil Provider")
		}
	}()
	_ = UnaryInterceptor(nil)
}

// --- StreamInterceptor -------------------------------------------------------

// fakeStream is a minimal grpc.ServerStream that just carries a ctx.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestStreamInterceptorPassesPrincipalToHandler(t *testing.T) {
	p := Principal{UserID: "bob", TenantID: "wayne", Mode: ModeCloud}
	rp := &recordingProvider{principal: p}
	interceptor := StreamInterceptor(rp)

	var gotPrincipal Principal
	handler := func(_ any, ss grpc.ServerStream) error {
		gp, ok := FromContext(ss.Context())
		if !ok {
			return errors.New("no principal")
		}
		gotPrincipal = gp
		return nil
	}
	inCtx := context.Background()
	err := interceptor(
		nil, &fakeStream{ctx: inCtx}, &grpc.StreamServerInfo{},
		handler,
	)
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if gotPrincipal != p {
		t.Errorf("got %+v, want %+v", gotPrincipal, p)
	}
}

// --- StaticJWTProvider -------------------------------------------------------

// signHS256 builds a JWT signed with the given secret and claims —
// matching the shape StaticJWTProvider expects to validate.
func signHS256(t *testing.T, secret []byte, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	return signed
}

func mdCtx(kv ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(kv...))
}

func TestStaticJWTHappyPath(t *testing.T) {
	secret := []byte("shh-this-is-a-test-only-secret-xyz")
	exp := time.Now().Add(5 * time.Minute).Unix()
	token := signHS256(t, secret, jwt.MapClaims{
		"sub":              "cognito-user-abc",
		"custom:tenant_id": "tenant-42",
		"aud":              "aegis-core",
		"exp":              exp,
	})
	p := StaticJWTProvider{Secret: secret, ExpectedAudience: "aegis-core"}
	got, err := p.Authenticate(mdCtx("authorization", "Bearer "+token))
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.UserID != "cognito-user-abc" || got.TenantID != "tenant-42" || got.Mode != ModeCloud {
		t.Errorf("Principal = %+v", got)
	}
}

func TestStaticJWTRejectsNoMetadata(t *testing.T) {
	p := StaticJWTProvider{Secret: []byte("x")}
	if _, err := p.Authenticate(context.Background()); err == nil {
		t.Fatal("accepted ctx with no metadata")
	}
}

func TestStaticJWTRejectsNoAuthorizationHeader(t *testing.T) {
	p := StaticJWTProvider{Secret: []byte("x")}
	if _, err := p.Authenticate(mdCtx()); err == nil {
		t.Fatal("accepted metadata without authorization")
	}
}

func TestStaticJWTRejectsNonBearer(t *testing.T) {
	p := StaticJWTProvider{Secret: []byte("x")}
	if _, err := p.Authenticate(mdCtx("authorization", "Basic dXNlcjpwYXNz")); err == nil {
		t.Fatal("accepted Basic auth")
	}
}

func TestStaticJWTRejectsWrongSecret(t *testing.T) {
	token := signHS256(t, []byte("right-secret"), jwt.MapClaims{"sub": "u1"})
	p := StaticJWTProvider{Secret: []byte("wrong-secret")}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with wrong HMAC key")
	}
}

func TestStaticJWTRejectsExpiredToken(t *testing.T) {
	secret := []byte("s")
	token := signHS256(t, secret, jwt.MapClaims{
		"sub": "u1",
		"exp": time.Now().Add(-5 * time.Minute).Unix(),
	})
	p := StaticJWTProvider{Secret: secret}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted expired token")
	}
}

func TestStaticJWTRejectsMissingSub(t *testing.T) {
	secret := []byte("s")
	// No "sub" claim at all.
	token := signHS256(t, secret, jwt.MapClaims{
		"custom:tenant_id": "t1",
		"exp":              time.Now().Add(5 * time.Minute).Unix(),
	})
	p := StaticJWTProvider{Secret: secret}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with missing sub claim")
	}
}

func TestStaticJWTRejectsAudienceMismatch(t *testing.T) {
	secret := []byte("s")
	token := signHS256(t, secret, jwt.MapClaims{
		"sub": "u1",
		"aud": "other-service",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	p := StaticJWTProvider{Secret: secret, ExpectedAudience: "aegis-core"}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+token)); err == nil {
		t.Fatal("accepted token with wrong audience")
	}
}

// TestStaticJWTRejectsAlgNone defends against the classic JWT "alg:
// none" downgrade attack. Our Keyfunc rejects any signing method that
// isn't HMAC.
func TestStaticJWTRejectsAlgNone(t *testing.T) {
	// Hand-craft a token with alg=none. jwt.New refuses to use
	// SigningMethodNone without explicit allowance, so we build the
	// raw string — this mirrors what a malicious client would send.
	token := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "attacker",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	raw, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("SignedString: %v", err)
	}
	p := StaticJWTProvider{Secret: []byte("any-secret")}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer "+raw)); err == nil {
		t.Fatal("accepted alg=none token — classic JWT downgrade attack succeeded")
	}
}

func TestStaticJWTRejectsEmptySecret(t *testing.T) {
	p := StaticJWTProvider{}
	if _, err := p.Authenticate(mdCtx("authorization", "Bearer anything")); err == nil {
		t.Fatal("accepted auth with no secret configured")
	}
}
