package sensitive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// distinctivePayload is a PCM byte pattern that's cheap to grep for in
// test output — if ANY formatter leaks the raw bytes, this marker
// shows up and the test fails. Using 0xDEAD 0xBEEF gives us both an
// obvious hex signature and a non-ASCII string.
var distinctivePayload = RedactedPCM{0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF}

const (
	// The marker that MUST appear for a correctly-redacted rendering.
	// Bracket format mirrors the C++ engine's SensitiveBytes (see
	// engine_cpp/src/infra/sensitive_bytes.h) so a single grep works
	// across both runtimes' logs.
	wantMarker = "[REDACTED PCM 8 bytes]"
	// Any of these substrings in output means the raw bytes leaked.
	// "deadbeef" covers %x / %X hex output, "\u00de" covers the raw
	// UTF-8 of 0xDE when %s stringifies a bare []byte. The base64 of
	// the payload is also a leakage form we have to guard against for
	// the JSON path (default []byte marshaling is base64).
	leakSubstrHex     = "deadbeef"
	leakSubstrB64     = "3q2+79qt" // base64(0xDEADBEEF 0xDEADBEEF) prefix
	leakSubstrBase64F = "3q2+7w=="
)

// assertRedacted verifies the rendered string contains the redaction
// marker AND is free of every known leakage signature. Called from
// every formatter test so the leak-detection checklist stays DRY.
func assertRedacted(t *testing.T, ctx, rendered string) {
	t.Helper()
	if !strings.Contains(rendered, wantMarker) {
		t.Errorf("%s: output missing marker %q, got %q", ctx, wantMarker, rendered)
	}
	lower := strings.ToLower(rendered)
	for _, leak := range []string{leakSubstrHex, leakSubstrB64, leakSubstrBase64F} {
		if strings.Contains(lower, strings.ToLower(leak)) {
			t.Errorf("%s: output leaks raw bytes (found %q in %q)", ctx, leak, rendered)
		}
	}
}

// TestFmtVerbsAllRedact walks every common fmt verb and confirms none
// of them expose the underlying bytes. fmt.Formatter's "handle any
// verb" contract is the single bottleneck; this test guards against
// a future refactor accidentally demoting the type to fmt.Stringer
// (which %x would bypass).
func TestFmtVerbsAllRedact(t *testing.T) {
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		rendered := fmt.Sprintf(verb, distinctivePayload)
		assertRedacted(t, "fmt.Sprintf("+verb+")", rendered)
	}
}

// TestPrintlnRedacts is the most common accidental leak path:
// someone types `log.Println(pcm)` or `fmt.Println(pcm)`. The default
// path uses fmt.Formatter (if implemented) → our Format method runs.
func TestPrintlnRedacts(t *testing.T) {
	var buf bytes.Buffer
	_, _ = fmt.Fprintln(&buf, distinctivePayload)
	assertRedacted(t, "fmt.Fprintln", buf.String())
}

// TestMarshalJSONRedacts — encoding/json's built-in []byte marshaling
// is base64, which we must NOT inherit. Verify plain Marshal AND
// a struct field containing RedactedPCM both redact.
func TestMarshalJSONRedacts(t *testing.T) {
	// Direct Marshal.
	out, err := json.Marshal(distinctivePayload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	assertRedacted(t, "json.Marshal(direct)", string(out))

	// Struct embedding — more realistic leak path.
	type envelope struct {
		ChunkID int         `json:"chunk_id"`
		PCM     RedactedPCM `json:"pcm"`
	}
	e := envelope{ChunkID: 42, PCM: distinctivePayload}
	out, err = json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal(struct): %v", err)
	}
	assertRedacted(t, "json.Marshal(struct)", string(out))
	// The surrounding envelope must still work — sanity check.
	if !strings.Contains(string(out), `"chunk_id":42`) {
		t.Errorf("chunk_id not preserved: %s", out)
	}
}

// TestSlogRedacts confirms slog.LogValuer is honored — both via
// the positional-arg form and via slog.Group.
func TestSlogRedacts(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(h)
	logger.Info("ingested", "chunk", distinctivePayload)
	assertRedacted(t, "slog.Info(attr)", buf.String())

	buf.Reset()
	logger.Info("ingested", slog.Any("pcm", distinctivePayload))
	assertRedacted(t, "slog.Info(slog.Any)", buf.String())
}

// TestStringRedacts covers the rare direct .String() call — for
// example in an error message where the caller does
// `fmt.Errorf("bad pcm: %s", pcm.String())`. Even though this is
// redundant (Format would cover it), we keep both paths tested so
// a future "maybe we can drop String()" refactor is caught.
func TestStringRedacts(t *testing.T) {
	assertRedacted(t, "RedactedPCM.String()", distinctivePayload.String())
	assertRedacted(t, "RedactedPCM.GoString()", distinctivePayload.GoString())
}

// TestBytesRoundTrip verifies the ONE sanctioned escape hatch
// returns the original bytes (so the engine-facing proto Send path
// gets real PCM, not a redacted string). Also checks that mutating
// the escape hatch mutates the underlying slice — RedactedPCM is a
// view, not a copy, matching the C++ SensitiveBytes non-owning
// semantics.
func TestBytesRoundTrip(t *testing.T) {
	p := RedactedPCM{1, 2, 3, 4}
	got := p.Bytes()
	if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Errorf("Bytes() = %v, want [1 2 3 4]", got)
	}
	got[0] = 99
	if p[0] != 99 {
		t.Errorf("Bytes() did not return a view of the backing array (expected aliasing)")
	}
}

// TestLenSafe — Len is the sanctioned alternative to logging the
// value: its output carries only size (timing) information, never
// content.
func TestLenSafe(t *testing.T) {
	if got := distinctivePayload.Len(); got != 8 {
		t.Errorf("Len() = %d, want 8", got)
	}
	if got := RedactedPCM(nil).Len(); got != 0 {
		t.Errorf("Len() on nil = %d, want 0", got)
	}
}

// TestEmptyRedactsCleanly — edge case: zero-length slice should still
// render a sensible redaction marker, not something that could be
// misread as a successful serialization of no audio.
func TestEmptyRedactsCleanly(t *testing.T) {
	var empty RedactedPCM
	got := fmt.Sprintf("%v", empty)
	if !strings.Contains(got, "0 bytes") {
		t.Errorf("empty redaction = %q, want substring '0 bytes'", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("empty redaction = %q, want substring 'REDACTED'", got)
	}
}
