package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/sensitive"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
)

// stubEngine is a minimal EngineServer that captures ingest messages
// for later assertion and drives egress messages on demand. The unit
// tests use this in place of the real C++ engine — the whole point of
// the Gateway-side tests is to exercise the pipeline's wiring, not the
// transcription model.
//
// Channels are buffered so test assertions don't race the stream
// goroutine; sendBuf is the egress channel the test writes to and the
// engine stub forwards to the client.
type stubEngine struct {
	aegisv1.UnimplementedEngineServer

	mu        sync.Mutex
	ingested  []*aegisv1.IngestMessage  // every message received from client
	startedCh chan struct{}             // closed when SessionStart observed
	endedCh   chan struct{}             // closed when CloseSend observed (io.EOF on Recv)
	sendBuf   chan *aegisv1.EgressMessage // tests drive egress via this
}

func newStubEngine() *stubEngine {
	return &stubEngine{
		startedCh: make(chan struct{}),
		endedCh:   make(chan struct{}),
		sendBuf:   make(chan *aegisv1.EgressMessage, 16),
	}
}

func (s *stubEngine) StreamTranscribe(stream aegisv1.Engine_StreamTranscribeServer) error {
	// Egress pump — forwards test-driven EgressMessages to the client.
	// Exits when the stream context cancels (which happens after this
	// handler returns, per gRPC semantics). Waiting on its exit from
	// the ingest loop would deadlock: egress waits on ctx.Done(), ctx
	// cancels only after handler returns, handler can't return while
	// waiting on egress. So we intentionally DON'T wait — the egress
	// goroutine is allowed to clean up lazily.
	go func() {
		for {
			select {
			case msg, ok := <-s.sendBuf:
				if !ok {
					return
				}
				if err := stream.Send(msg); err != nil {
					return
				}
			case <-stream.Context().Done():
				return
			}
		}
	}()

	// Ingest pump — runs on the handler goroutine; returning from this
	// function closes the stream.
	var startedOnce sync.Once
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				close(s.endedCh)
				return nil
			}
			return err
		}
		s.mu.Lock()
		s.ingested = append(s.ingested, msg)
		s.mu.Unlock()
		if msg.GetSessionStart() != nil {
			startedOnce.Do(func() { close(s.startedCh) })
		}
	}
}

func (s *stubEngine) ingestedCopy() []*aegisv1.IngestMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*aegisv1.IngestMessage, len(s.ingested))
	copy(out, s.ingested)
	return out
}

// setupStubEngine spins up a bufconn-backed engine and returns a
// connected EngineClient plus the stub so tests can inspect ingress
// and drive egress. cleanup closes everything.
func setupStubEngine(t *testing.T) (aegisv1.EngineClient, *stubEngine, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	stub := newStubEngine()
	aegisv1.RegisterEngineServer(srv, stub)
	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn serve: %v", err)
		}
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("grpc.NewClient: %v", err)
	}
	client := aegisv1.NewEngineClient(conn)
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return client, stub, cleanup
}

func newTestSession(t *testing.T) *session.Session {
	t.Helper()
	reg := session.NewRegistry()
	sess, err := reg.Create(session.Config{
		RAGID:         "rag-42",
		Title:         "pipeline-test",
		LanguageHints: []string{"en", "zh-Hant"},
	})
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	return sess
}

// waitFor polls fn until it returns true or the timeout expires. Used
// to avoid sleeping in assertions about cross-goroutine state.
func waitFor(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestNewSendsSessionStart(t *testing.T) {
	engine, stub, cleanup := setupStubEngine(t)
	defer cleanup()

	sess := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	defer p.Close()

	select {
	case <-stub.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for SessionStart")
	}

	got := stub.ingestedCopy()
	if len(got) == 0 {
		t.Fatal("no ingest messages captured")
	}
	ss := got[0].GetSessionStart()
	if ss == nil {
		t.Fatalf("first ingest message is not SessionStart: %+v", got[0])
	}
	if ss.GetSessionId() != sess.ID {
		t.Errorf("session_id = %q, want %q", ss.GetSessionId(), sess.ID)
	}
	if ss.GetRagId() != "rag-42" {
		t.Errorf("rag_id = %q, want %q", ss.GetRagId(), "rag-42")
	}
	if got := ss.GetLanguageHints(); len(got) != 2 || got[0] != "en" || got[1] != "zh-Hant" {
		t.Errorf("language_hints = %v, want [en zh-Hant]", got)
	}
	af := ss.GetAudioFormat()
	if af == nil {
		t.Fatal("SessionStart missing AudioFormat")
	}
	if af.GetSampleRateHz() != 16000 || af.GetChannels() != 1 || af.GetBitsPerSample() != 16 {
		t.Errorf("AudioFormat = %+v, want 16000/1/16", af)
	}
}

func TestSendControlReachesEngine(t *testing.T) {
	engine, stub, cleanup := setupStubEngine(t)
	defer cleanup()

	sess := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	defer p.Close()

	// Wait for SessionStart so the PAUSE we send below is guaranteed
	// to land as the SECOND ingest message.
	<-stub.startedCh

	if err := p.SendControl(aegisv1.ControlKind_CONTROL_KIND_PAUSE); err != nil {
		t.Fatalf("SendControl: %v", err)
	}

	ok := waitFor(t, time.Second, func() bool {
		got := stub.ingestedCopy()
		if len(got) < 2 {
			return false
		}
		ctl := got[1].GetControl()
		return ctl != nil && ctl.GetKind() == aegisv1.ControlKind_CONTROL_KIND_PAUSE
	})
	if !ok {
		t.Fatalf("PAUSE control event never observed: %+v", stub.ingestedCopy())
	}
}

func TestEgressTranscriptIsBroadcast(t *testing.T) {
	engine, stub, cleanup := setupStubEngine(t)
	defer cleanup()

	sess := newTestSession(t)

	// Subscribe BEFORE the pipeline starts so we can't miss a race
	// between engine egress and viewer subscription.
	events, unsubscribe := sess.Subscribe(8)
	defer unsubscribe()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	defer p.Close()

	<-stub.startedCh

	// Drive an egress transcript and verify it fans out through
	// session.Broadcast.
	transcript := &aegisv1.TranscriptSegment{
		SegmentId:    1,
		SpeakerLabel: "Host",
		StartMs:      0,
		EndMs:        1000,
		Text:         "hello pipeline",
		Language:     "en",
		IsFinal:      true,
	}
	stub.sendBuf <- &aegisv1.EgressMessage{
		Payload: &aegisv1.EgressMessage_Transcript{Transcript: transcript},
	}

	select {
	case ev := <-events:
		ts := ev.GetTranscript()
		if ts == nil {
			t.Fatalf("ViewerEvent is not a transcript: %+v", ev)
		}
		if ts.GetText() != "hello pipeline" {
			t.Errorf("text = %q, want %q", ts.GetText(), "hello pipeline")
		}
		if !ts.GetIsFinal() {
			t.Errorf("is_final = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broadcast ViewerEvent")
	}
}

func TestCloseIsIdempotentAndSendsEndStream(t *testing.T) {
	engine, stub, cleanup := setupStubEngine(t)
	defer cleanup()

	sess := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}

	<-stub.startedCh

	p.Close()
	p.Close() // second call must not panic or hang.

	select {
	case <-stub.endedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after Pipeline.Close")
	}

	// Verify at least one END_STREAM control event was observed
	// before CloseSend.
	gotEnd := false
	for _, m := range stub.ingestedCopy() {
		if c := m.GetControl(); c != nil && c.GetKind() == aegisv1.ControlKind_CONTROL_KIND_END_STREAM {
			gotEnd = true
			break
		}
	}
	if !gotEnd {
		t.Fatal("END_STREAM control event never sent")
	}
}

// dialLiveEngine sets up the client to an already-running engine.
// Returns a ready EngineClient and a cleanup that closes the conn.
// Skips the test (via t.Skip) when AEGIS_ENGINE_ADDR is unset — both
// live-engine tests use this so CI with no engine stays green.
func dialLiveEngine(t *testing.T) (aegisv1.EngineClient, func()) {
	t.Helper()
	addr := os.Getenv("AEGIS_ENGINE_ADDR")
	if addr == "" {
		t.Skip("AEGIS_ENGINE_ADDR not set; skipping live engine integration")
	}
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial engine at %s: %v", addr, err)
	}
	engine := aegisv1.NewEngineClient(conn)

	// Sanity-check reachability before any bidi stream opens — a Health
	// error here fails fast with a clearer message than a mid-stream
	// CONNECTION_REFUSED a few seconds in.
	healthCtx, healthCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer healthCancel()
	if _, err := engine.Health(healthCtx, &aegisv1.HealthRequest{}); err != nil {
		_ = conn.Close()
		t.Fatalf("engine Health at %s: %v", addr, err)
	}
	return engine, func() { _ = conn.Close() }
}

// TestStreamTranscribeIntegration exercises the pipeline lifecycle
// against a real engine. It is gated on AEGIS_ENGINE_ADDR; the heavier
// WAV-based transcription test is TestTranscribeJFKLiveEngine below.
// This one only verifies that SessionStart + END_STREAM round-trip
// cleanly — useful as a fast smoke test when the WAV fixture is absent.
func TestStreamTranscribeIntegration(t *testing.T) {
	engine, cleanup := dialLiveEngine(t)
	defer cleanup()

	sess := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}

	// Close triggers END_STREAM + CloseSend; runEgress then drains
	// whatever the engine flushed (likely nothing without audio) and
	// exits, unblocking Close.
	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline.Close did not return within 5s against live engine")
	}
}

// resolveJFKWAV locates the jfk.wav test fixture at run time.
//
// Order of resolution:
//  1. AEGIS_JFK_WAV env override — for ad-hoc `go test` runs outside
//     Bazel (direct IDE execution, etc.).
//  2. TEST_SRCDIR + known repo-mapping spellings — Bazel surfaces the
//     @whisper_cpp//:samples filegroup here (see data dep in BUILD.bazel).
//     The exact directory name depends on Bazel version's repo-mapping
//     scheme; we try both classic (~) and modern (+) encodings.
//  3. Empty string — test skips with a helpful message.
func resolveJFKWAV(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("AEGIS_JFK_WAV"); p != "" {
		return p
	}
	root := os.Getenv("TEST_SRCDIR")
	if root == "" {
		return ""
	}
	// Bazel 7 (classic ~ mapping) and Bazel 8+ (new + mapping). Leaving
	// both in rather than chasing the active version keeps the test
	// portable across bazelisk rolls.
	candidates := []string{
		filepath.Join(root, "_main~_repo_rules~whisper_cpp", "samples", "jfk.wav"),
		filepath.Join(root, "+_repo_rules+whisper_cpp", "samples", "jfk.wav"),
		filepath.Join(root, "_main+_repo_rules+whisper_cpp", "samples", "jfk.wav"),
		filepath.Join(root, "whisper_cpp", "samples", "jfk.wav"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// TestTranscribeJFKLiveEngine drives real audio through Pipeline.WritePCM
// against a live engine and asserts the emitted transcript contains
// the same phrases the C++ engine's stream_transcribe_test checks for.
// Matching the two assertions at the phrase level means: if this test
// flakes but the C++ one passes, the regression lives in the Go gateway
// pipeline — not in whisper.cpp.
//
// Run manually:
//
//	./tools/bazelisk/bazelisk run  //engine_cpp/cmd/engine:engine   # in one shell
//	AEGIS_ENGINE_ADDR=localhost:50051 \
//	  ./tools/bazelisk/bazelisk test //gateway_go/internal/pipeline:pipeline_test \
//	  --test_env=AEGIS_ENGINE_ADDR --test_output=errors
//
// Skips when AEGIS_ENGINE_ADDR is unset or jfk.wav cannot be located.
func TestTranscribeJFKLiveEngine(t *testing.T) {
	engine, cleanup := dialLiveEngine(t)
	defer cleanup()

	wavPath := resolveJFKWAV(t)
	if wavPath == "" {
		t.Skip("jfk.wav not located (set AEGIS_JFK_WAV or run under Bazel with @whisper_cpp//:samples in data deps)")
	}
	wav, err := ReadWAV(wavPath)
	if err != nil {
		t.Fatalf("ReadWAV(%s): %v", wavPath, err)
	}
	if wav.SampleRateHz != 16000 || wav.Channels != 1 || wav.BitsPerSample != 16 {
		t.Fatalf("unexpected WAV format: %d Hz / %d ch / %d bits (want 16000/1/16)",
			wav.SampleRateHz, wav.Channels, wav.BitsPerSample)
	}

	sess := newTestSession(t)

	// Subscribe BEFORE the pipeline opens so no broadcast races the
	// subscription. Buffer is sized to cover typical JFK clip output
	// (< 10 interim + final segments for ~11 s of audio).
	events, unsubscribe := sess.Subscribe(32)
	defer unsubscribe()

	// Generous ctx — whisper.cpp's tiny.en on CPU still takes a few
	// seconds to emit final segments after all audio has landed.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p, err := New(ctx, engine, sess, sess.ID)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}

	// Feed PCM in 32 KB chunks — matches the C++ test's kChunkBytes
	// so we exercise the same Send cadence the engine was tuned for.
	// Not real-time (no sleep): engine state machine handles
	// back-pressure internally; the test wants fast turn-around.
	const chunkBytes = 32 * 1024
	for off := 0; off < len(wav.Data); off += chunkBytes {
		end := off + chunkBytes
		if end > len(wav.Data) {
			end = len(wav.Data)
		}
		if err := p.WritePCM(sensitive.RedactedPCM(wav.Data[off:end])); err != nil {
			t.Fatalf("WritePCM at offset %d: %v", off, err)
		}
	}

	// Half-close the ingest side so the engine flushes any pending
	// final segment. This is the same sequence EndMeeting invokes in
	// production via Pipeline.Close.
	closed := make(chan struct{})
	go func() {
		p.Close()
		close(closed)
	}()

	// Collect ViewerEvents until the egress pump closes the channel
	// (Close returned → runEgress exited → but we're subscribed to
	// session, which stays open unless MarkEnded was called) OR until
	// Close finishes and a grace period elapses. The grace window
	// catches final segments that land slightly after Close().
	var collected []*aegisv1.ViewerEvent
	deadline := time.After(55 * time.Second)
	grace := time.Duration(0)
collectLoop:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				break collectLoop
			}
			collected = append(collected, ev)
		case <-closed:
			// Stream to engine is half-closed; allow 2 s for tail
			// segments to arrive before giving up.
			if grace == 0 {
				grace = 2 * time.Second
			}
			select {
			case ev, ok := <-events:
				if !ok {
					break collectLoop
				}
				collected = append(collected, ev)
			case <-time.After(grace):
				break collectLoop
			case <-deadline:
				t.Fatal("deadline exceeded waiting for transcript egress")
			}
		case <-deadline:
			t.Fatal("deadline exceeded waiting for transcript egress")
		}
	}

	// Concatenate the text of every transcript segment the engine
	// emitted. Both interim and final segments count — the JFK
	// utterance is short enough that either one should carry the
	// assertable tokens.
	var sb strings.Builder
	transcripts := 0
	for _, ev := range collected {
		if ts := ev.GetTranscript(); ts != nil {
			sb.WriteString(ts.GetText())
			sb.WriteString(" ")
			transcripts++
		}
	}
	all := strings.ToLower(sb.String())
	t.Logf("collected %d ViewerEvents (%d transcripts); text=%q", len(collected), transcripts, all)

	if transcripts == 0 {
		t.Fatal("engine returned zero TranscriptSegments")
	}
	// The phrases are those the C++ stream_transcribe_test also
	// asserts on (jfk.wav transcript = "And so, my fellow Americans,
	// ask not what your country can do for you, ask what you can do
	// for your country."). If this ever changes, update both tests.
	if !strings.Contains(all, "ask not") {
		t.Errorf("transcript missing 'ask not'; got: %q", all)
	}
	if !strings.Contains(all, "your country") {
		t.Errorf("transcript missing 'your country'; got: %q", all)
	}
}

// Compile-time assertion: Pipeline exposes the three methods the
// factory closure in cmd/gateway/main.go depends on. A future refactor
// that renames any of these should fail here rather than at the
// (much slower) binary build step.
var _ = func() error {
	var p *Pipeline
	if p == nil {
		return nil
	}
	if err := p.WriteRTPPayload(nil); err != nil {
		return fmt.Errorf("unexpected: %w", err)
	}
	if err := p.SendControl(aegisv1.ControlKind_CONTROL_KIND_PAUSE); err != nil {
		return err
	}
	p.Close()
	return nil
}
