// Package sensitive provides type wrappers that prevent accidental
// leakage of sensitive payload data into logs, traces, metrics, panic
// messages, or any other observability sink.
//
// ADR-0005 R3 ("log formatter type whitelist") draws a hard line:
// audio PCM bytes must never appear anywhere outside the direct path
// from capture to the transcription engine. The canonical enforcement
// mechanism is a distinct type whose String / Format / JSON / slog
// implementations refuse to print the bytes — so even a careless
//
//	log.Printf("pcm=%v", pcm)
//
// cannot leak the raw audio. The engine-side C++ equivalent is
// `aegis::infra::SensitiveBytes` (engine_cpp/src/infra/sensitive_bytes.h);
// this package is the Go mirror.
//
// Callers access the raw bytes via an explicit .Bytes() method. That
// call site IS the ADR-0005 audit point: code review and (future)
// Semgrep rules in tools/ci/semgrep_rules/ treat .Bytes() as
// whitelistable only in the engine-facing gRPC Send path and the
// decoder output path. Anywhere else the grep result is a bug.
package sensitive

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// RedactedPCM wraps raw 16 kHz mono 16-bit-LE PCM bytes with format /
// log / JSON interface implementations that refuse to print the
// underlying audio.
//
// Use it as the parameter type for any function that accepts PCM: the
// type signature itself communicates "this is sensitive" and the
// compiler then prevents a plain []byte from being passed accidentally.
//
// Conversion from []byte is an explicit cast at the allocation site
// (typically right after Opus decode or WAV parsing), keeping the
// construction points small and auditable.
type RedactedPCM []byte

// redactionOf is the canonical redaction string. Callers should get
// the same text regardless of which interface path rendered it, so a
// grep for "REDACTED PCM" across log aggregates will find every
// occurrence — not a subset.
//
// Format choice: "[REDACTED PCM N bytes]" mirrors
// engine_cpp/src/infra/sensitive_bytes.h:67 ("[REDACTED N bytes]") so
// observability tooling can use one regex across both runtimes. The
// square brackets (instead of the more natural "<>") also sidestep
// encoding/json's HTML-escape behavior for "<" / ">" — otherwise
// JSON output would render "\u003c..\u003e" and a log grep for the
// marker would miss it.
func redactionOf(r RedactedPCM) string {
	return fmt.Sprintf("[REDACTED PCM %d bytes]", len(r))
}

// Format implements fmt.Formatter. Handling ALL verbs (%v, %s, %x,
// %#v, %+v, ...) at the Formatter level is deliberate: fmt.Stringer
// alone only covers %v/%s — %x would otherwise fall back to the
// default []byte formatter and hex-dump the payload, and %#v would
// reveal the struct internals. Formatter is the single bottleneck.
func (r RedactedPCM) Format(s fmt.State, verb rune) {
	// The verb is intentionally ignored. Redaction is unconditional.
	_, _ = fmt.Fprint(s, redactionOf(r))
}

// String implements fmt.Stringer. Kept alongside Format for callers
// that invoke .String() directly (e.g. error wrapping via %w won't
// touch Format but may touch Stringer if the value is Stringer'd at
// a different layer).
func (r RedactedPCM) String() string {
	return redactionOf(r)
}

// MarshalJSON implements json.Marshaler. encoding/json's default for
// []byte is base64 — still leakage. Override with a plain redaction
// string. This also means a RedactedPCM field inside a struct encoded
// via json.Marshal never produces the raw bytes.
func (r RedactedPCM) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactionOf(r))
}

// LogValue implements slog.LogValuer. Go 1.21+ structured logging
// picks this up when the value is passed as an attribute; the record
// carries the redaction string, not the bytes.
func (r RedactedPCM) LogValue() slog.Value {
	return slog.StringValue(redactionOf(r))
}

// GoString implements fmt.GoStringer for %#v consistency. The
// Formatter above already covers %#v, but some code paths (reflect-
// based pretty-printers) call GoString directly.
func (r RedactedPCM) GoString() string {
	return redactionOf(r)
}

// Len returns the payload length in bytes. Always safe to log — the
// length carries no audio content, only timing information.
func (r RedactedPCM) Len() int {
	return len(r)
}

// Bytes returns the underlying []byte. This is the ONLY sanctioned
// escape hatch. Every call site must be reviewable and should map to
// one of:
//
//  1. Serializing to the engine's PcmChunk.Pcm proto field (the single
//     Gateway → Engine gRPC boundary); or
//  2. Feeding the whisper decoder once the data has reached its final
//     destination (engine-side only; this Go package has no such path).
//
// Anywhere else a call to .Bytes() appears in a log / metric / trace
// path is an ADR-0005 violation. Reviewers should flag it; Semgrep
// rules (TODO, tracked alongside the C++ SensitiveBytes.bytes() rule
// in tools/ci/semgrep_rules/) will eventually automate that.
func (r RedactedPCM) Bytes() []byte {
	return []byte(r)
}
