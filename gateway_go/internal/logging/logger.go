// Package logging provides a small factory for the Gateway's
// `*slog.Logger` configured from environment variables.
//
// Go 1.21+ established `log/slog` as the stdlib structured-logging
// surface; pairing it with the `slog.LogValuer` implementations on
// our domain types (see `internal/sensitive.RedactedPCM`) means
// sensitive data is structurally incapable of appearing in log
// output regardless of the call site's discipline.
//
// Environment variables:
//
//   AEGIS_LOG_FORMAT   "json" (default) | "text"
//                      JSON is the production default — machine-
//                      parseable by Fluent Bit, Loki, CloudWatch Logs
//                      Insights, etc. Text is for interactive dev.
//
//   AEGIS_LOG_LEVEL    "debug" | "info" (default) | "warn" | "error"
//                      Honored at handler level; messages below the
//                      threshold are a no-op (no formatting cost).
//
// The logger writes to os.Stderr. stdout is deliberately not used —
// it's reserved for machine-parseable program output that consumers
// (tests, pipelines) may want to read without log-noise interleave.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a fresh *slog.Logger configured from env. Callers that
// only need the process-wide default logger should use SetDefault
// instead — New is for packages that want an independently-scoped
// logger (with `.With(...)` baked-in fields, for example).
func New() *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(os.Getenv("AEGIS_LOG_LEVEL"))}
	var h slog.Handler
	switch strings.ToLower(os.Getenv("AEGIS_LOG_FORMAT")) {
	case "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	default:
		// JSON is the default — a non-"text" value (including empty,
		// "json", or anything else) selects it. We don't validate
		// further because the cost of a one-character typo in an
		// env var is a known format (JSON) rather than a startup
		// failure.
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// SetDefault initializes slog.Default() from env and returns the same
// logger for callers that want an explicit handle. Idempotent — calling
// it again resets slog.Default() to a fresh logger, which tests can
// exploit for isolation.
func SetDefault() *slog.Logger {
	l := New()
	slog.SetDefault(l)
	return l
}

// parseLevel maps the AEGIS_LOG_LEVEL spelling to a slog.Level. Unknown
// values fall back to LevelInfo silently — the portfolio-grade
// tradeoff here is "one typo still ships" over "one typo breaks
// startup".
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
