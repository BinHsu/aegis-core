// Package pipeline wires the live audio path from a WebRTC RTP stream
// through the Opus decoder to the C++ engine's StreamTranscribe gRPC
// stream, and fans transcript egress out to viewers via
// session.Session.Broadcast.
//
// Data flow (Phase 2 A4):
//
//	OnTrack (pion)                ↓ []byte Opus payload
//	Negotiator.AudioChan()        ↓
//	cmd/gateway factory closure   ↓ (calls pipeline.WriteRTPPayload)
//	Pipeline.WriteRTPPayload      ↓ Opus → int16 PCM (pion/opus, 16 kHz mono)
//	StreamTranscribe.Send         ↓ IngestMessage{Pcm}
//	  (engine processes)          ↓
//	StreamTranscribe.Recv         ↓ EgressMessage{Transcript}
//	Pipeline.runEgress            ↓ ViewerEvent{Transcript}
//	Session.Broadcast             ↓ fan-out
//	JoinAsViewer / ws.Handler    ↓ (per-subscription renumber)
//	  viewer stream
//
// Architectural notes:
//
//   - This package depends on aegisv1 (proto), session, and pion/opus;
//     it does NOT depend on internal/grpc or internal/webrtc. The
//     factory-injection pattern (internal/grpc.Config.AudioPipelineStart)
//     keeps the gRPC service free of this dependency graph, which in
//     turn keeps its test binary tiny.
//
//   - pion/opus can emit 16 kHz mono PCM directly via its internal
//     resampler (NewDecoderWithOutput(16000, 1)), so we avoid a manual
//     48 kHz stereo → 16 kHz mono downmix path.
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

	"github.com/pion/opus"
	"google.golang.org/protobuf/types/known/timestamppb"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/sensitive"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
)

// Audio format constants — the canonical MVP format per ADR-0011. Any
// divergence here must also be reflected in the engine-side validation
// and the pion/opus decoder initialization.
const (
	sampleRateHz  = 16000
	channels      = 1
	bitsPerSample = 16

	// pcmDecodeBufSamples sizes the reusable int16 buffer passed into
	// opus.DecodeToInt16. An Opus packet carries at most 120 ms of
	// audio; at 48 kHz stereo that's 120*48*2 = 11 520 samples. We
	// allocate a generous 12 288 to avoid any tight-fit reallocation
	// risk. (NewDecoderWithOutput(16000, 1) makes the actual output
	// smaller, but the underlying buffers inside pion/opus use the
	// pre-resample sample count.)
	pcmDecodeBufSamples = 12288
)

// Pipeline owns one engine StreamTranscribe bidi stream for the lifetime
// of a session. Construct via New; tear down via Close.
type Pipeline struct {
	sess      *session.Session
	sessionID string
	stream    aegisv1.Engine_StreamTranscribeClient
	startedAt time.Time

	// dec is NOT safe for concurrent use; all WriteRTPPayload calls
	// run on the single pump goroutine in main.go's factory closure.
	// We do not guard dec with a mutex because that goroutine is the
	// sole caller of WriteRTPPayload. Stored by value because pion's
	// method set uses pointer receivers and *Pipeline makes &p.dec
	// trivially addressable.
	dec opus.Decoder

	// Reusable decode scratch — avoids per-packet allocation on the hot
	// path. Only touched by WriteRTPPayload (single goroutine).
	pcmScratch []int16

	// sendMu guards the gRPC stream's Send method. gRPC requires
	// Send/CloseSend to be serialized but concurrent with Recv.
	sendMu sync.Mutex

	// chunkSeq is the monotonic PcmChunk chunk_id (see aegis.proto).
	// Touched only by WriteRTPPayload (single goroutine).
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

	dec, err := opus.NewDecoderWithOutput(sampleRateHz, channels)
	if err != nil {
		return nil, fmt.Errorf("pipeline: opus decoder init: %w", err)
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
		sess:       sess,
		sessionID:  sessionID,
		stream:     stream,
		startedAt:  time.Now(),
		dec:        dec,
		pcmScratch: make([]int16, pcmDecodeBufSamples),
		done:       make(chan struct{}),
	}
	go p.runEgress()
	return p, nil
}

// WriteRTPPayload decodes an Opus-encoded RTP payload (as produced by
// Negotiator.AudioChan) into 16 kHz mono int16 PCM and sends it as a
// PcmChunk to the engine.
//
// Must be called from a single goroutine — the decoder and chunk
// sequencer are not concurrency-safe. The factory closure in main.go
// is the sole caller in production.
//
// An empty payload is a no-op. A decode error is returned to the caller
// but does NOT tear down the stream — a single corrupt Opus frame
// should not kill an otherwise-healthy session.
func (p *Pipeline) WriteRTPPayload(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	// p.dec is a value; method has a pointer receiver — Go auto-takes
	// the address via &p.dec since p is *Pipeline.
	n, err := p.dec.DecodeToInt16(payload, p.pcmScratch)
	if err != nil {
		return fmt.Errorf("pipeline: opus decode: %w", err)
	}
	if n <= 0 {
		return nil
	}
	// DecodeToInt16 returns samples-per-channel. Output is mono, so
	// total samples == n.
	pcmBytes := make([]byte, n*2)
	for i := 0; i < n; i++ {
		s := p.pcmScratch[i]
		pcmBytes[i*2] = byte(s)
		pcmBytes[i*2+1] = byte(s >> 8)
	}
	// Wrap as RedactedPCM immediately at the decode boundary so a
	// plain []byte can't flow further — ADR-0005 R3 enforcement.
	return p.WritePCM(sensitive.RedactedPCM(pcmBytes))
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
// Same single-goroutine contract as WriteRTPPayload: the chunk
// sequence counter is not atomic. sendMu handles concurrency with
// SendControl / Close only.
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
	p.chunkSeq++

	p.sendMu.Lock()
	defer p.sendMu.Unlock()
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
