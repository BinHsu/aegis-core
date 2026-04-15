// Package pipeline wires the live audio path from a WebRTC RTP stream
// to the C++ engine's StreamTranscribe gRPC stream, and fans transcript
// egress out to viewers via session.Session.Broadcast.
//
// Data flow (Phase 2 A4, post ADR-0016):
//
//	OnTrack (pion)                ↓ []byte Opus payload
//	Negotiator.AudioChan()        ↓
//	cmd/gateway factory closure   ↓ (calls pipeline.WriteRTPPayload)
//	Pipeline.WriteRTPPayload      ↓ forwards Opus verbatim, no decode
//	StreamTranscribe.Send         ↓ IngestMessage{Opus}
//	  (engine decodes + transcribes — ADR-0016)
//	StreamTranscribe.Recv         ↓ EgressMessage{Transcript}
//	Pipeline.runEgress            ↓ ViewerEvent{Transcript}
//	Session.Broadcast             ↓ fan-out
//	JoinAsViewer / ws.Handler    ↓ (per-subscription renumber)
//	  viewer stream
//
// Architectural notes:
//
//   - This package depends on aegisv1 (proto) and session; it does
//     NOT depend on internal/grpc or internal/webrtc. The
//     factory-injection pattern (internal/grpc.Config.AudioPipelineStart)
//     keeps the gRPC service free of this dependency graph, which in
//     turn keeps its test binary tiny.
//
//   - Opus decoding was previously done here via pion/opus. Per
//     ADR-0016 it moved to the C++ engine (libopus) after Phase 3
//     live-phone testing revealed pion/opus's mode-3 gap against real
//     WebRTC traffic. The gateway now forwards the RTP payload
//     verbatim; codec work lives where the audio-processing domain
//     lives, not in the BFF.
//
//   - The send side is mutex-protected because WriteRTPPayload and
//     SendControl are called concurrently from the factory-closure
//     pump goroutine AND from RPC handlers (Stop on EndMeeting).
//     gRPC client streams are NOT safe for concurrent Send calls.
//
//   - The egress goroutine runs until Recv returns io.EOF or any error
//     (normal EndMeeting path: the engine drains and closes after
//     observing ControlKind_END_STREAM). The `done` channel lets
//     Close block until egress has exited and closed the stream.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/sensitive"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
)

// Audio format constants — the canonical MVP format per ADR-0011. These
// describe the engine's *post-decode* format; the on-wire chunk over
// gRPC is Opus (via OpusChunk) on the live WebRTC path and int16 LE
// PCM (via PcmChunk) on the fixture-replay / push-to-talk path.
const (
	sampleRateHz  = 16000
	channels      = 1
	bitsPerSample = 16
)

// Pipeline owns one engine StreamTranscribe bidi stream for the lifetime
// of a session. Construct via New; tear down via Close.
type Pipeline struct {
	sess      *session.Session
	sessionID string
	stream    aegisv1.Engine_StreamTranscribeClient
	startedAt time.Time

	// sendMu guards the gRPC stream's Send method. gRPC requires
	// Send/CloseSend to be serialized but concurrent with Recv.
	sendMu sync.Mutex

	// chunkSeq is the monotonic audio-chunk sequence number shared by
	// WriteRTPPayload (OpusChunk) and WritePCM (PcmChunk). The proto
	// (aegis.proto) permits independent counters per variant, but a
	// single session only uses one audio source in practice, so a
	// shared counter keeps the sequence monotonic across whatever
	// path is active. Touched only by the send path (serialized by
	// sendMu).
	chunkSeq uint64

	// done is closed by runEgress when it exits; Close waits on it so
	// the caller can rely on "no more Broadcast calls after Close".
	done chan struct{}

	// closed guards the idempotence of Close.
	closeOnce sync.Once
}

// New opens a StreamTranscribe bidi stream to the engine, sends the
// SessionStart header, spins up the egress goroutine, and returns a
// ready-to-use Pipeline.
//
// The caller is responsible for invoking Close exactly once — on normal
// shutdown (EndMeeting) or on abnormal termination (process signal).
// Close is idempotent.
//
// ctx scopes the stream lifetime. Production wires the process-level
// signal context; tests wire their own per-test ctx.
func New(
	ctx context.Context,
	engine aegisv1.EngineClient,
	sess *session.Session,
	sessionID string,
) (*Pipeline, error) {
	if engine == nil {
		return nil, fmt.Errorf("pipeline: engine client required")
	}
	if sess == nil {
		return nil, fmt.Errorf("pipeline: session required")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("pipeline: sessionID required")
	}

	stream, err := engine.StreamTranscribe(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline: StreamTranscribe open: %w", err)
	}

	start := &aegisv1.IngestMessage{
		Payload: &aegisv1.IngestMessage_SessionStart{
			SessionStart: &aegisv1.SessionStart{
				SessionId:     sessionID,
				TenantId:      sess.TenantID,
				RagId:         sess.RAGID,
				LanguageHints: append([]string(nil), sess.LanguageHints...),
				AudioFormat: &aegisv1.AudioFormat{
					SampleRateHz:  sampleRateHz,
					Channels:      channels,
					BitsPerSample: bitsPerSample,
				},
			},
		},
	}
	if err := stream.Send(start); err != nil {
		// Best-effort close; the caller will see the error from Send.
		_ = stream.CloseSend()
		return nil, fmt.Errorf("pipeline: SessionStart: %w", err)
	}

	p := &Pipeline{
		sess:      sess,
		sessionID: sessionID,
		stream:    stream,
		startedAt: time.Now(),
		done:      make(chan struct{}),
	}
	go p.runEgress()
	return p, nil
}

// WriteRTPPayload forwards an Opus-encoded RTP payload (as produced by
// Negotiator.AudioChan) to the engine as an OpusChunk. Per ADR-0016,
// the gateway does NOT decode: codec work lives in the C++ engine
// alongside whisper.cpp. An empty payload is a no-op.
//
// Safe to call concurrently with SendControl / Close; sendMu serializes
// the underlying gRPC Send calls.
//
// ADR-0005 R3 / ADR-0016 note on wrapper types: Opus frames carry
// voice that reconstructs to an audible signal, so they ARE sensitive
// data. We do NOT wrap them in a `sensitive.RedactedOpus` type
// (matching the PcmChunk pattern) because the gateway's audit surface
// is a single call site — this function. A Semgrep rule on
// IngestMessage_Opus accesses outside this file is cheaper and clearer
// than a wrapper type whose unwrap-on-proto-Send boundary would be
// identical to the one below. See aegis.proto's OpusChunk comment.
func (p *Pipeline) WriteRTPPayload(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	offset := time.Since(p.startedAt).Milliseconds()
	msg := &aegisv1.IngestMessage{
		Payload: &aegisv1.IngestMessage_Opus{
			Opus: &aegisv1.OpusChunk{
				Opus:     payload,
				ChunkId:  p.chunkSeq,
				OffsetMs: offset,
			},
		},
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	p.chunkSeq++
	if err := p.stream.Send(msg); err != nil {
		return fmt.Errorf("pipeline: send Opus: %w", err)
	}
	return nil
}

// WritePCM sends a PCM chunk (already 16 kHz mono 16-bit little-endian,
// matching SessionStart's AudioFormat) to the engine. The direct-
// injection path for callers that already have decoded PCM —
// integration tests using pre-recorded WAV fixtures, or future
// non-WebRTC audio sources (e.g. a host push-to-talk WebSocket).
//
// The sensitive.RedactedPCM parameter type is ADR-0005 R3 enforcement:
// callers that try to pass a plain []byte get a compile error. The
// .Bytes() unwrap on the proto Send line is the single auditable
// leak point — any .Bytes() call elsewhere in this tree is a review
// flag.
//
// Safe to call concurrently with WriteRTPPayload / SendControl / Close;
// sendMu serializes the underlying gRPC Send calls.
func (p *Pipeline) WritePCM(pcm sensitive.RedactedPCM) error {
	if pcm.Len() == 0 {
		return nil
	}
	offset := time.Since(p.startedAt).Milliseconds()
	msg := &aegisv1.IngestMessage{
		Payload: &aegisv1.IngestMessage_Pcm{
			Pcm: &aegisv1.PcmChunk{
				// ADR-0005 R3 audit point: this is the sanctioned
				// unwrap — PcmChunk.Pcm is the proto-generated []byte
				// field that proto serialization requires.
				Pcm:      pcm.Bytes(),
				ChunkId:  p.chunkSeq,
				OffsetMs: offset,
			},
		},
	}

	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	p.chunkSeq++
	if err := p.stream.Send(msg); err != nil {
		return fmt.Errorf("pipeline: send PCM: %w", err)
	}
	return nil
}

// SendControl forwards a ControlKind to the engine (PAUSE / RESUME /
// END_STREAM). Safe for concurrent use with WriteRTPPayload.
func (p *Pipeline) SendControl(kind aegisv1.ControlKind) error {
	msg := &aegisv1.IngestMessage{
		Payload: &aegisv1.IngestMessage_Control{
			Control: &aegisv1.ControlEvent{Kind: kind},
		},
	}
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if err := p.stream.Send(msg); err != nil {
		return fmt.Errorf("pipeline: send control %v: %w", kind, err)
	}
	return nil
}

// Close performs a graceful shutdown: sends END_STREAM (best-effort),
// half-closes the ingest side, and blocks until runEgress returns. Safe
// to call multiple times.
//
// The caller should NOT invoke WriteRTPPayload or SendControl after
// Close; doing so races with CloseSend and may return an error from
// the gRPC runtime. The pump goroutine in main.go's factory closure
// observes its pipeCtx cancellation and exits before Close returns to
// its caller.
func (p *Pipeline) Close() {
	p.closeOnce.Do(func() {
		// Best-effort END_STREAM so the engine can flush any pending
		// transcript before the stream teardown. Errors here are
		// expected if the stream is already broken.
		_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_END_STREAM)
		p.sendMu.Lock()
		_ = p.stream.CloseSend()
		p.sendMu.Unlock()
		<-p.done
	})
}

// runEgress reads EgressMessages from the engine and broadcasts the
// transcript variant as a ViewerEvent. Runs until the stream returns
// io.EOF (engine closed its send side — normal termination) or any
// error.
//
// Closing p.done on exit is the synchronization point Close waits on.
func (p *Pipeline) runEgress() {
	defer close(p.done)
	for {
		msg, err := p.stream.Recv()
		if err != nil {
			// io.EOF is the normal END_STREAM path; other errors
			// (context cancellation, stream reset) are also terminal
			// — in either case, no more broadcasts are possible.
			if err == io.EOF {
				return
			}
			return
		}
		switch payload := msg.GetPayload().(type) {
		case *aegisv1.EgressMessage_Transcript:
			if payload.Transcript == nil {
				continue
			}
			p.sess.Broadcast(&aegisv1.ViewerEvent{
				// Sequence is set per-subscription by the viewer
				// handler (JoinAsViewer / ws.Handler) — leaving it
				// zero here avoids ambiguity.
				EmittedAt: timestamppb.New(time.Now()),
				Payload: &aegisv1.ViewerEvent_Transcript{
					Transcript: payload.Transcript,
				},
			})
		case *aegisv1.EgressMessage_Hint:
			if payload.Hint == nil {
				continue
			}
			p.sess.Broadcast(&aegisv1.ViewerEvent{
				EmittedAt: timestamppb.New(time.Now()),
				Payload: &aegisv1.ViewerEvent_Hint{
					Hint: payload.Hint,
				},
			})
		default:
			// EgressMessage_Status and EgressMessage_Error are not yet
			// fanned out to viewers; later phases may emit state
			// change or error-telemetry events. For now they are
			// consumed silently so the stream continues draining.
		}
	}
}
