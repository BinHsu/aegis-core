package logging

import (
	"log/slog"
	"os"
	"testing"
)

func TestParseLevelDefaultsInfo(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want slog.Level
	}{
		{"", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"bogus", slog.LevelInfo}, // unknown → default info
	} {
		if got := parseLevel(tc.in); got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNewReturnsLogger — thin smoke test that New() actually builds a
// usable logger from any env permutation. We're not trying to re-test
// slog's handler internals here; just that parseLevel + format
// dispatch don't explode.
func TestNewReturnsLogger(t *testing.T) {
	restore := swapEnv(t, "AEGIS_LOG_FORMAT", "text")
	defer restore()
	l := New()
	if l == nil {
		t.Fatal("New() returned nil")
	}
	// Log at Info — should not panic or error.
	l.Info("smoke", "k", "v")
}

func TestSetDefaultIdempotent(t *testing.T) {
	// Save the original default so other tests aren't affected.
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })

	a := SetDefault()
	b := SetDefault()
	// Each call should return a fresh logger — the test isn't about
	// identity, it's about "calling twice doesn't panic and both
	// results are usable."
	if a == nil || b == nil {
		t.Fatal("SetDefault returned nil")
	}
	a.Info("a")
	b.Info("b")
}

// swapEnv sets k=v for the duration of the test, restoring the prior
// value (or unset state) on cleanup. Tiny helper that keeps
// AEGIS_LOG_* env munging contained.
func swapEnv(t *testing.T, k, v string) func() {
	t.Helper()
	prev, had := os.LookupEnv(k)
	if err := os.Setenv(k, v); err != nil {
		t.Fatalf("setenv %s: %v", k, err)
	}
	return func() {
		if had {
			_ = os.Setenv(k, prev)
		} else {
			_ = os.Unsetenv(k)
		}
	}
}
