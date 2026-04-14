// Package webrtc wraps github.com/pion/webrtc/v4 with the gateway's
// session-scoped lifetime model.
//
// Phase 2 A3 scope: receive an SDP offer from the host client, perform
// a non-trickle ICE exchange, and return the answer SDP. OnTrack
// increments a per-session RTP packet counter as proof-of-life.
//
// Phase 2 A4 scope: bridge incoming RTP (Opus-encoded audio) and ICE
// connection-state transitions to channel-based consumers. The
// audio-pipeline wiring lives in cmd/gateway/main.go and
// internal/pipeline — keeping this package free of any pipeline /
// engine gRPC imports (the factory-injection seam is in
// internal/grpc.GatewayService.pipelineStart).
//
// The WebRTCNegotiator interface consumed by the grpc package is defined
// in //gateway_go/internal/grpc — *Negotiator satisfies it via duck
// typing. Tests in the grpc package inject a stub without importing this
// Pion-backed package, keeping that package's test binary free of the
// Pion dependency graph.
package webrtc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	pwebrtc "github.com/pion/webrtc/v4"
)

// Per-session channel buffer sizes. Chosen so a briefly-stalled consumer
// (e.g. a slow engine gRPC Send) does not immediately starve the RTP
// reader. On overflow we drop — audio jitter is preferable to back-
// pressuring the Pion read loop, which would mangle ICE/DTLS timings.
const (
	audioChanBuffer = 64 // ≈ 1.3 s of 20 ms Opus frames
	iceChanBuffer   = 8
)

// ICEState is the small subset of Pion's ICEConnectionState that the
// audio pipeline actually reacts to. Translated by the Negotiator so
// consumers do not import Pion just to observe pause/resume signals.
type ICEState int

const (
	// ICEStateConnected fires on Pion's Connected OR Completed
	// transitions — the pipeline treats both as "resume ingest".
	ICEStateConnected ICEState = iota
	// ICEStateDisconnected fires on Pion's Disconnected — transient;
	// ADR-0006 grace window applies, pipeline sends PAUSE to engine.
	ICEStateDisconnected
	// ICEStateFailed fires on Pion's Failed — terminal; pipeline
	// sends END_STREAM and stops.
	ICEStateFailed
)

// connState holds the PeerConnection, the per-session RTP reception
// counter, and the outbound channels drained by the audio pipeline.
// The struct must not be copied (atomic.Uint64 is non-copyable); always
// store and pass as *connState.
type connState struct {
	pc      *pwebrtc.PeerConnection
	packets atomic.Uint64

	// audio receives one element per RTP packet that carried a non-empty
	// payload — the raw Opus-encoded bytes, stripped of the RTP header.
	// The pipeline decodes these into 16 kHz mono PCM.
	//
	// Full-buffer writes are dropped (select/default) rather than
	// blocked: jitter is preferable to stalling the Pion read loop.
	audio chan []byte

	// ice receives translated ICE-state transitions for the audio
	// pipeline. Same drop-on-full policy as `audio`: the pipeline's
	// pause/resume ordering is advisory, not strictly monotonic.
	ice chan ICEState
}

// Negotiator manages one PeerConnection per session and satisfies the
// WebRTCNegotiator interface defined in //gateway_go/internal/grpc.
// The zero value is NOT usable — always call New.
type Negotiator struct {
	api   *pwebrtc.API // nil → use pion default; set via newWithAPI test seam
	mu    sync.Mutex
	conns map[string]*connState
}

// New returns a Negotiator using pion's default configuration.
// No STUN/TURN servers are configured — ADR-0007 assumes same-LAN
// operation where host ICE candidates suffice.
func New() *Negotiator {
	return &Negotiator{conns: make(map[string]*connState)}
}

// newWithAPI is the test seam: callers supply a *pwebrtc.API backed by a
// virtual network so tests can run without real UDP sockets.
func newWithAPI(api *pwebrtc.API) *Negotiator {
	return &Negotiator{api: api, conns: make(map[string]*connState)}
}

// Negotiate performs a non-trickle SDP exchange for sessionID.
//
//  1. Creates a PeerConnection (closing any stale one for the same
//     session — handles re-negotiation).
//  2. Allocates per-session audio + ICE channels and registers OnTrack
//     (forwards RTP payloads) and OnICEConnectionStateChange (forwards
//     translated state transitions). Both registered before
//     SetRemoteDescription so no event is missed.
//  3. Sets the caller's offer as the remote description.
//  4. Creates and sets the local answer.
//  5. Waits until ICE gathering completes — all host candidates are
//     embedded in the returned SDP. Trickle ICE is NOT supported in
//     this phase (ADR-0007 local-LAN assumption).
//  6. Returns the final SDP with inline candidates.
//
// If ctx expires during ICE gathering, the PeerConnection is closed and
// ctx.Err() is returned.
func (n *Negotiator) Negotiate(ctx context.Context, sessionID, offerSDP string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("negotiate: sessionID required")
	}
	if offerSDP == "" {
		return "", fmt.Errorf("negotiate: offerSDP required")
	}

	pc, err := n.newPeerConnection()
	if err != nil {
		return "", fmt.Errorf("negotiate: NewPeerConnection: %w", err)
	}

	cs := &connState{
		pc:    pc,
		audio: make(chan []byte, audioChanBuffer),
		ice:   make(chan ICEState, iceChanBuffer),
	}

	// Register OnTrack before SetRemoteDescription so we never miss a
	// track that the remote side starts immediately on signaling.
	pc.OnTrack(func(track *pwebrtc.TrackRemote, _ *pwebrtc.RTPReceiver) {
		for {
			pkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				return // PeerConnection closed or track ended.
			}
			cs.packets.Add(1)
			if len(pkt.Payload) == 0 {
				continue
			}
			// Copy out of the Pion-owned buffer: ReadRTP reuses the
			// same backing array across iterations, so we hand the
			// consumer an independent slice they can hold as long as
			// they like.
			payload := make([]byte, len(pkt.Payload))
			copy(payload, pkt.Payload)
			select {
			case cs.audio <- payload:
			default:
				// Drop: consumer is too slow, audio jitter is the
				// lesser evil.
			}
		}
	})

	pc.OnICEConnectionStateChange(func(state pwebrtc.ICEConnectionState) {
		var mapped ICEState
		switch state {
		case pwebrtc.ICEConnectionStateConnected,
			pwebrtc.ICEConnectionStateCompleted:
			mapped = ICEStateConnected
		case pwebrtc.ICEConnectionStateDisconnected:
			mapped = ICEStateDisconnected
		case pwebrtc.ICEConnectionStateFailed,
			pwebrtc.ICEConnectionStateClosed:
			mapped = ICEStateFailed
		default:
			// New / Checking: not actionable for the pipeline.
			return
		}
		select {
		case cs.ice <- mapped:
		default:
			// Drop: pause/resume ordering is advisory.
		}
	})

	// Replace any pre-existing PeerConnection for this session (e.g.
	// host re-negotiating after a reconnect). Close the old one first
	// to free ICE sockets.
	n.mu.Lock()
	if old, ok := n.conns[sessionID]; ok {
		_ = old.pc.Close()
	}
	n.conns[sessionID] = cs
	n.mu.Unlock()

	if err := pc.SetRemoteDescription(pwebrtc.SessionDescription{
		Type: pwebrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		n.closeOne(sessionID)
		return "", fmt.Errorf("negotiate: SetRemoteDescription: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		n.closeOne(sessionID)
		return "", fmt.Errorf("negotiate: CreateAnswer: %w", err)
	}

	// GatheringCompletePromise must be created BEFORE SetLocalDescription
	// to avoid a race where gathering completes synchronously (common on
	// loopback) and the channel is never closed.
	gatherDone := pwebrtc.GatheringCompletePromise(pc)

	if err := pc.SetLocalDescription(answer); err != nil {
		n.closeOne(sessionID)
		return "", fmt.Errorf("negotiate: SetLocalDescription: %w", err)
	}

	// Block until all host ICE candidates are gathered or the caller
	// cancels. The returned SDP then contains all candidates inline.
	select {
	case <-gatherDone:
	case <-ctx.Done():
		n.closeOne(sessionID)
		return "", fmt.Errorf("negotiate: ICE gathering: %w", ctx.Err())
	}

	return pc.LocalDescription().SDP, nil
}

// PacketsReceived returns the number of RTP packets received on any
// incoming audio track for sessionID. Returns 0 if the session is
// not present (unknown or already closed). Safe for concurrent use.
func (n *Negotiator) PacketsReceived(sessionID string) uint64 {
	n.mu.Lock()
	cs, ok := n.conns[sessionID]
	n.mu.Unlock()
	if !ok {
		return 0
	}
	return cs.packets.Load()
}

// AudioChan returns the receive-only channel of Opus-encoded RTP payloads
// for sessionID. Returns nil if the session is unknown — a nil channel
// in a select case blocks forever, which composes correctly with the
// pipeline goroutine's ctx.Done() escape.
//
// The channel is NOT closed by the Negotiator on session end; consumers
// are expected to also watch a cancellation signal (typically the
// pipeline-scoped context passed through the factory closure in
// main.go). This keeps the OnTrack callback free of close-after-close
// panics when a PeerConnection tears down faster than the consumer.
func (n *Negotiator) AudioChan(sessionID string) <-chan []byte {
	n.mu.Lock()
	cs, ok := n.conns[sessionID]
	n.mu.Unlock()
	if !ok {
		return nil
	}
	return cs.audio
}

// ICEChan returns the receive-only channel of ICEState transitions for
// sessionID. Same nil-on-unknown / never-closed contract as AudioChan.
func (n *Negotiator) ICEChan(sessionID string) <-chan ICEState {
	n.mu.Lock()
	cs, ok := n.conns[sessionID]
	n.mu.Unlock()
	if !ok {
		return nil
	}
	return cs.ice
}

// Close tears down the PeerConnection for sessionID, releasing the
// underlying ICE sockets and DTLS state. Called by EndMeeting so
// resources are freed promptly. No-op if sessionID is unknown.
//
// The audio/ice channels are NOT closed (see AudioChan docs) — consumers
// exit via their own context cancellation. Closing pc.Close() causes
// the OnTrack reader to unblock with an error and return, so no further
// writes are attempted regardless.
func (n *Negotiator) Close(sessionID string) error {
	return n.closeOne(sessionID)
}

func (n *Negotiator) closeOne(sessionID string) error {
	n.mu.Lock()
	cs, ok := n.conns[sessionID]
	if ok {
		delete(n.conns, sessionID)
	}
	n.mu.Unlock()
	if !ok {
		return nil
	}
	return cs.pc.Close()
}

func (n *Negotiator) newPeerConnection() (*pwebrtc.PeerConnection, error) {
	cfg := pwebrtc.Configuration{
		ICEServers: []pwebrtc.ICEServer{}, // No STUN/TURN on LAN (ADR-0007).
	}
	if n.api != nil {
		return n.api.NewPeerConnection(cfg)
	}
	return pwebrtc.NewPeerConnection(cfg)
}
