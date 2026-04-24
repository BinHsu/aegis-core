// frontend_web/internal/auth/oidc_integration_test.go
//
// Phase 4e-4 integration coverage per ADR-0034 §D4. Drives a real
// AWS Cognito User Pool (ldz-provisioned per aegis-core#76) end-to-end:
//
//   1. AdminCreateUser  — create an ephemeral test user with the
//                         required `custom:tenant_id` attribute
//   2. AdminSetUserPassword — set permanent password (skip the
//                             default FORCE_CHANGE_PASSWORD state)
//   3. AdminInitiateAuth ADMIN_USER_PASSWORD_AUTH — get a real
//                                                   ID token
//   4. OIDCProvider.Authenticate against the live JWKS — verify
//      end-to-end signature + claim mapping matches what the
//      gateway's CLOUD-mode interceptor would do per-RPC
//   5. AdminDeleteUser — cleanup; ldz #76 D-answer chose the
//                        ephemeral pattern over fixed pre-seeded
//
// SKIP behavior: if AEGIS_COGNITO_USER_POOL_ID is not set in the
// environment, the test skips. That makes this file safe to run
// from `bazelisk test //gateway_go/internal/auth:auth_test` on a
// developer laptop without AWS credentials — only the nightly
// CI workflow at .github/workflows/nightly-cognito-integration.yml
// sets the env and exercises the live path.
//
// Env vars consumed:
//
//   AEGIS_COGNITO_USER_POOL_ID   e.g. "eu-central-1_0gdyxKxOB"
//   AEGIS_COGNITO_APP_CLIENT_ID  e.g. "1e7i376p7bitfup27gsqklg2hu"
//   AEGIS_COGNITO_ISSUER_URL     e.g. "https://cognito-idp.eu-central-1.amazonaws.com/eu-central-1_0gdyxKxOB"
//   AWS_REGION                   e.g. "eu-central-1"
//   AWS_*                        STS-issued temporary creds from
//                                the workflow's OIDC role assumption
//
// All five values surfaced from the ldz #76 reply 2026-04-24 + ldz's
// SSM Parameter Store at /aegis/staging/cognito/* mirrors the same
// values.

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"google.golang.org/grpc/metadata"
)

const (
	// Mailbox-invalid TLD per RFC 6761; nothing routes to it, no risk
	// of accidental email delivery during nightly runs even if the
	// User Pool's verification flow tries to send mail. Cognito treats
	// `email` purely as an attribute string in the ADMIN_USER_PASSWORD_AUTH
	// flow we drive here, so the TLD only matters for the "what
	// happens if someone misconfigures verification" scenario.
	integrationTestEmailDomain = "example.invalid"

	// Tenant scope assigned to every nightly test user. Distinct from
	// any production tenant so audit logs can grep `it-nightly-` to
	// isolate test traffic from real meeting traffic.
	integrationTestTenantID = "it-nightly-tenant"
)

func TestOIDCIntegrationCognito(t *testing.T) {
	poolID := os.Getenv("AEGIS_COGNITO_USER_POOL_ID")
	clientID := os.Getenv("AEGIS_COGNITO_APP_CLIENT_ID")
	issuerURL := os.Getenv("AEGIS_COGNITO_ISSUER_URL")
	if poolID == "" || clientID == "" || issuerURL == "" {
		t.Skip(
			"Cognito integration test skipped — set " +
				"AEGIS_COGNITO_USER_POOL_ID + AEGIS_COGNITO_APP_CLIENT_ID + " +
				"AEGIS_COGNITO_ISSUER_URL (live values surfaced via " +
				"ldz Terraform outputs / SSM PS /aegis/staging/cognito/*) " +
				"to exercise the real path. See " +
				".github/workflows/nightly-cognito-integration.yml.",
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("aws config.LoadDefaultConfig: %v", err)
	}
	cidp := cognitoidentityprovider.NewFromConfig(awsCfg)

	username := newEphemeralUsername(t)
	password := newEphemeralPassword(t)

	t.Logf("integration: creating ephemeral user %s", username)

	// --- (1) AdminCreateUser ---------------------------------------------
	//
	// MessageAction = SUPPRESS — Cognito would otherwise try to mail
	// the user a verification message; suppressing keeps the nightly
	// run free of outbound mail attempts to the .invalid TLD.
	if _, err := cidp.AdminCreateUser(ctx, &cognitoidentityprovider.AdminCreateUserInput{
		UserPoolId:    aws.String(poolID),
		Username:      aws.String(username),
		MessageAction: cidptypes.MessageActionTypeSuppress,
		UserAttributes: []cidptypes.AttributeType{
			{Name: aws.String("email"), Value: aws.String(username)},
			{Name: aws.String("email_verified"), Value: aws.String("true")},
			{Name: aws.String("custom:tenant_id"), Value: aws.String(integrationTestTenantID)},
		},
	}); err != nil {
		t.Fatalf("AdminCreateUser: %v", err)
	}

	// Cleanup runs unconditionally — even if a later step fails, the
	// test user is removed so the User Pool doesn't accumulate orphans.
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanCancel()
		if _, err := cidp.AdminDeleteUser(cleanCtx, &cognitoidentityprovider.AdminDeleteUserInput{
			UserPoolId: aws.String(poolID),
			Username:   aws.String(username),
		}); err != nil {
			t.Logf("integration cleanup: AdminDeleteUser %s failed: %v "+
				"(orphan; manual cleanup may be required)", username, err)
		}
	})

	// --- (2) AdminSetUserPassword ---------------------------------------
	//
	// Permanent=true skips the FORCE_CHANGE_PASSWORD challenge state
	// AdminCreateUser leaves users in by default. Without this, step
	// (3) returns NEW_PASSWORD_REQUIRED instead of an actual token.
	if _, err := cidp.AdminSetUserPassword(ctx, &cognitoidentityprovider.AdminSetUserPasswordInput{
		UserPoolId: aws.String(poolID),
		Username:   aws.String(username),
		Password:   aws.String(password),
		Permanent:  true,
	}); err != nil {
		t.Fatalf("AdminSetUserPassword: %v", err)
	}

	// --- (3) AdminInitiateAuth ------------------------------------------
	//
	// ADMIN_USER_PASSWORD_AUTH is the flow ldz #76 §B explicitly
	// enabled on the app client (`ALLOW_ADMIN_USER_PASSWORD_AUTH`).
	// The non-admin USER_PASSWORD_AUTH flow would also work but
	// requires the SECRET_HASH dance; admin flow is operator-only and
	// auth-flow-clean.
	authResp, err := cidp.AdminInitiateAuth(ctx, &cognitoidentityprovider.AdminInitiateAuthInput{
		UserPoolId: aws.String(poolID),
		ClientId:   aws.String(clientID),
		AuthFlow:   cidptypes.AuthFlowTypeAdminUserPasswordAuth,
		AuthParameters: map[string]string{
			"USERNAME": username,
			"PASSWORD": password,
		},
	})
	if err != nil {
		t.Fatalf("AdminInitiateAuth: %v", err)
	}
	if authResp.AuthenticationResult == nil ||
		authResp.AuthenticationResult.IdToken == nil {
		t.Fatalf("AdminInitiateAuth returned no IdToken (challenge: %v)",
			authResp.ChallengeName)
	}
	idToken := *authResp.AuthenticationResult.IdToken

	// --- (4) OIDCProvider.Authenticate ----------------------------------
	//
	// Build the same OIDCProvider the gateway constructs at startup
	// in CLOUD mode (cmd/gateway/main.go buildAuthProvider). Issuer
	// is the only thing that varies across environments; everything
	// else is the prod-shape default.
	provider, err := NewOIDCProvider(ctx, OIDCConfig{
		Issuer:   issuerURL,
		Audience: clientID, // Cognito ID-token aud == app client ID
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}

	mdCtx := metadata.NewIncomingContext(
		ctx,
		metadata.Pairs("authorization", "Bearer "+idToken),
	)
	principal, err := provider.Authenticate(mdCtx)
	if err != nil {
		t.Fatalf("OIDCProvider.Authenticate against live Cognito ID token: %v", err)
	}

	// --- (5) Principal assertions ---------------------------------------
	if principal.UserID == "" {
		t.Errorf("Principal.UserID empty — Cognito `sub` claim should populate it")
	}
	if principal.TenantID != integrationTestTenantID {
		t.Errorf("Principal.TenantID = %q, want %q (custom:tenant_id we set at AdminCreateUser)",
			principal.TenantID, integrationTestTenantID)
	}
	if principal.Mode != ModeCloud {
		t.Errorf("Principal.Mode = %q, want %q", principal.Mode, ModeCloud)
	}

	t.Logf("integration: live-Cognito Principal verified: UserID=%s TenantID=%s",
		principal.UserID, principal.TenantID)
}

// --- helpers ---------------------------------------------------------------

// newEphemeralUsername mints a unique test username so concurrent
// nightly runs (or retries within one run) never collide on the
// AdminCreateUser idempotency check.
func newEphemeralUsername(t *testing.T) string {
	t.Helper()
	id := randomHex(t, 8)
	return fmt.Sprintf("it-nightly-%s@%s", id, integrationTestEmailDomain)
}

// newEphemeralPassword mints a per-run password meeting Cognito's
// default policy (≥ 8 chars, requires upper / lower / digit / symbol).
// The password lives only for the duration of one test run + cleanup,
// then the user is deleted; no rotation discipline needed.
func newEphemeralPassword(t *testing.T) string {
	t.Helper()
	// 16 random hex bytes (32 chars) + a fixed safety prefix that
	// guarantees the policy mix. Cognito doesn't allowlist symbols —
	// `#$_` is a stable subset most pool defaults accept.
	return "Aegis#" + randomHex(t, 16) + "_It"
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(buf)
}
