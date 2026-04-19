package cors

import (
	"testing"
)

func TestPolicyPermissiveByDefault(t *testing.T) {
	t.Setenv(EnvVar, "")
	p := New()
	if !p.Permissive() {
		t.Fatalf("expected permissive policy when env unset, got strict")
	}
	for _, origin := range []string{
		"",
		"http://localhost:5173",
		"https://aegis-app.staging.binhsu.org",
		"https://attacker.example.com",
	} {
		if !p.Allow(origin) {
			t.Errorf("permissive policy rejected origin %q", origin)
		}
	}
}

func TestPolicyStrictAllowlist(t *testing.T) {
	t.Setenv(EnvVar, "https://aegis-app.staging.binhsu.org,https://aegis-app.prod.binhsu.org")
	p := New()
	if p.Permissive() {
		t.Fatalf("expected strict policy when env set, got permissive")
	}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://aegis-app.staging.binhsu.org", true},
		{"https://aegis-app.prod.binhsu.org", true},
		{"https://aegis-app.staging.binhsu.org/", false}, // trailing slash differs
		{"http://aegis-app.staging.binhsu.org", false},   // scheme differs
		{"https://attacker.example.com", false},
		{"", false},
		{"null", false},
	}
	for _, tc := range cases {
		if got := p.Allow(tc.origin); got != tc.want {
			t.Errorf("Allow(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

func TestPolicyTrimsWhitespaceAndIgnoresEmpties(t *testing.T) {
	t.Setenv(EnvVar, "  https://aegis-app.staging.binhsu.org , , https://aegis-app.prod.binhsu.org  ")
	p := New()
	if p.Permissive() {
		t.Fatalf("expected strict policy")
	}
	for _, origin := range []string{
		"https://aegis-app.staging.binhsu.org",
		"https://aegis-app.prod.binhsu.org",
	} {
		if !p.Allow(origin) {
			t.Errorf("Allow(%q) = false, want true (whitespace should be trimmed)", origin)
		}
	}
}

func TestPolicyEnvAllWhitespaceFallsBackToPermissive(t *testing.T) {
	// An env var of "  ,  ,  " has no real entries; rather than refuse
	// every origin (which would surprise the operator), the policy
	// falls back to permissive. Prevents footgun where a malformed
	// config silently bricks the gateway.
	t.Setenv(EnvVar, "  ,  ,  ")
	p := New()
	if !p.Permissive() {
		t.Fatalf("expected permissive fallback when env is all whitespace/commas")
	}
}
