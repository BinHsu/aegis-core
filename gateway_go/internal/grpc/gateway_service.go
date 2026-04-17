// Package grpc wires the Go Gateway's gRPC server-side handlers.
//
// This file implements aegis.v1.Gateway — the client-facing service
// hosted by the Gateway. Phases completed:
//
//   - CreateMeeting    — allocates a Session in the registry, issues a
//     viewer join JWT, reports engine metadata from the engine's
//     Health RPC. (A2)
//   - EndMeeting       — removes the session from the registry; calls
//     WebRTCNegotiator.Close to release ICE sockets. (A2, extended A3)
//   - JoinAsViewer     — token-validated server stream; fans out
//     transcript events via Session.Subscribe. (A2, A5)
//   - NegotiateWebRTC  — non-trickle SDP exchange via WebRTCNegotiator.
//     When no negotiator is configured (Config.WebRTCNegotiator == nil),
//     returns UNIMPLEMENTED. (A3)
//
// Naming note: we avoid importing this package as plain `grpc` because
// it collides with google.golang.org/grpc at call sites. Consumers
// typically alias it as `gatewaygrpc` or call New directly.
package grpc

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
)

// healthProber is the narrow subset of aegisv1.EngineClient that this
// service actually invokes. Tests supply a fake implementation so
// they don't have to stand up a full gRPC client.
type healthProber func(ctx context.Context) (*aegisv1.HealthResponse, error)

// WebRTCNegotiator is the interface injected into GatewayService for
// the SDP-exchange and PeerConnection lifecycle. The concrete type is
// *webrtc.Negotiator from //gateway_go/internal/webrtc; tests supply a
// stub without importing the Pion-backed package.
//
// Nil is a valid value: NegotiateWebRTC returns UNIMPLEMENTED and
// EndMeeting skips the Close call.
type WebRTCNegotiator interface {
	Negotiate(ctx context.Context, sessionID, offerSDP string) (answerSDP string, err error)
	Close(sessionID string) error
}

// AudioPipelineStartFn is the factory that NegotiateWebRTC calls after
// a successful SDP exchange to spin up the audio → engine → viewers
// pipeline for the session. Returning stopFn transfers pipeline
// ownership back to the service, which calls stopFn on EndMeeting.
//
// The concrete implementation lives in cmd/gateway/main.go so the
// factory can close over the Negotiator's audio/ICE channels and the
// engine client without this package importing internal/webrtc or
// internal/pipeline (keeping the gRPC test binary free of the pion
// dep graph — ADR-0013 isolation principle applied to Go deps).
//
// Nil is valid: NegotiateWebRTC still returns its answer SDP but no
// audio is ever piped to the engine. The Gateway degrades gracefully
// to "WebRTC works, transcription silent" instead of failing the RPC.
type AudioPipelineStartFn func(sess *session.Session, sessionID string) (stopFn func(), err error)

// GatewayService implements aegisv1.GatewayServer.
type GatewayService struct {
	aegisv1.UnimplementedGatewayServer

	registry   *session.Registry
	issuer     *token.Issuer
	health     healthProber
	webrtcNeg  WebRTCNegotiator     // nil → NegotiateWebRTC returns UNIMPLEMENTED
	pipelineStart AudioPipelineStartFn // nil → no audio pipeline on NegotiateWebRTC

	// pipelineStops tracks per-session stopFns returned by pipelineStart.
	// Populated in NegotiateWebRTC, drained in EndMeeting. Using sync.Map
	// instead of a mutex-guarded map because the load/store pattern is
	// rare (once per session lifetime) and the key (sessionID) is unique.
	pipelineStops sync.Map // map[string]func()

	engineProbeTimeout time.Duration
}

// Config bundles the dependencies of GatewayService so main.go wires
// them explicitly rather than reaching into package state.
type Config struct {
	Registry *session.Registry
	Issuer   *token.Issuer
	Engine   aegisv1.EngineClient

	// WebRTCNegotiator handles SDP exchange and PeerConnection lifetime
	// for the NegotiateWebRTC RPC. If nil, NegotiateWebRTC returns
	// UNIMPLEMENTED and EndMeeting does not call Close. Production sets
	// this to *webrtc.Negotiator from //gateway_go/internal/webrtc.
	WebRTCNegotiator WebRTCNegotiator

	// AudioPipelineStart is invoked at the tail of a successful
	// NegotiateWebRTC to bring up the audio pipeline for the session.
	// If nil, no pipeline is started (transcription is silent) but the
	// RPC still returns the answer SDP. See AudioPipelineStartFn docs.
	AudioPipelineStart AudioPipelineStartFn

	// EngineProbeTimeout applies to the Health call made during
	// CreateMeeting. Defaults to 2s when zero (matches the /healthz
	// budget in main.go).
	EngineProbeTimeout time.Duration
}

// New constructs a GatewayService ready to be registered with a
// grpc.Server via aegisv1.RegisterGatewayServer.
func New(cfg Config) (*GatewayService, error) {
	if cfg.Registry == nil {
		return nil, errors.New("gateway grpc: Registry is required")
	}
	if cfg.Issuer == nil {
		return nil, errors.New("gateway grpc: Issuer is required")
	}
	if cfg.Engine == nil {
		return nil, errors.New("gateway grpc: Engine client is required")
	}
	engine := cfg.Engine
	svc := newWithProber(cfg.Registry, cfg.Issuer,
		func(ctx context.Context) (*aegisv1.HealthResponse, error) {
			return engine.Health(ctx, &aegisv1.HealthRequest{})
		},
		cfg.EngineProbeTimeout)
	svc.webrtcNeg = cfg.WebRTCNegotiator
	svc.pipelineStart = cfg.AudioPipelineStart
	return svc, nil
}

// newWithProber is the test seam: it bypasses the engine gRPC client
// so unit tests can drive Health directly.
func newWithProber(
	registry *session.Registry,
	issuer *token.Issuer,
	health healthProber,
	timeout time.Duration,
) *GatewayService {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &GatewayService{
		registry:           registry,
		issuer:             issuer,
		health:             health,
		engineProbeTimeout: timeout,
	}
}

// CreateMeeting is the entrypoint for a new session.
//
// Error mapping (aligned with proto/aegis/v1/aegis.proto):
//   - INVALID_ARGUMENT   — empty rag_id, or title over 200 chars.
//   - UNAVAILABLE        — engine Health RPC failed entirely.
//   - RESOURCE_EXHAUSTED — engine reports !Ready (A4 swaps this for
//     the real ResourceBudget reservation).
//   - INTERNAL           — registry Create or JWT sign failed.
//
// UNAUTHENTICATED / PERMISSION_DENIED paths land in A5 with the
// Cognito JWT middleware; Local mode has no caller identity today.
func (s *GatewayService) CreateMeeting(
	ctx context.Context,
	req *aegisv1.CreateMeetingRequest,
) (*aegisv1.CreateMeetingResponse, error) {
	if req == nil || req.GetRagId() == "" {
		return nil, status.Error(codes.InvalidArgument, "rag_id is required")
	}
	if len(req.GetTitle()) > 200 {
		return nil, status.Error(codes.InvalidArgument, "title exceeds 200 characters")
	}

	probeCtx, cancel := context.WithTimeout(ctx, s.engineProbeTimeout)
	defer cancel()
	health, err := s.health(probeCtx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "engine Health: %v", err)
	}
	if !health.GetReady() {
		return nil, status.Error(codes.ResourceExhausted, "engine not ready")
	}

	sess, err := s.registry.Create(session.Config{
		TenantID:                "", // Local mode (ADR-0007 L7); A5 wires Cognito.
		RAGID:                   req.GetRagId(),
		Title:                   req.GetTitle(),
		LanguageHints:           req.GetLanguageHints(),
		AllowedViewerAccountIDs: req.GetAllowedViewerAccountIds(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "registry.Create: %v", err)
	}

	raw, err := s.issuer.Issue(sess.ID, sess.TokenExpiresAt)
	if err != nil {
		// Rollback — don't leak a session the caller cannot join.
		_ = s.registry.Delete(sess.ID)
		return nil, status.Errorf(codes.Internal, "issuer.Issue: %v", err)
	}

	var engineInfo *aegisv1.EngineInfo
	if info := health.GetInfo(); info != nil {
		engineInfo = &aegisv1.EngineInfo{
			Model:   info.GetModel(),
			Backend: info.GetBackend(),
			Version: info.GetVersion(),
		}
	}

	return &aegisv1.CreateMeetingResponse{
		SessionId:        sess.ID,
		ViewerJoinToken:  raw,
		TokenExpiresAt:   timestamppb.New(sess.TokenExpiresAt),
		SessionExpiresAt: timestamppb.New(sess.ExpiresAt),
		Engine:           engineInfo,
	}, nil
}

// EndMeeting terminates a session on the Gateway. For A2 it only
// touches the registry; A4 extends this to push CONTROL_KIND_END_STREAM
// into the engine and to close fan-out to viewers with a
// MEETING_STATE_ENDED event.
func (s *GatewayService) EndMeeting(
	ctx context.Context,
	req *aegisv1.EndMeetingRequest,
) (*aegisv1.EndMeetingResponse, error) {
	if req == nil || req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	sess, err := s.registry.Get(req.GetSessionId())
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return nil, status.Error(codes.NotFound, "session not found")
		}
		return nil, status.Errorf(codes.Internal, "registry.Get: %v", err)
	}
	if sess.Ended() {
		return nil, status.Error(codes.FailedPrecondition, "session already ended")
	}

	// PERMISSION_DENIED (host-only check) requires caller identity
	// from the Cognito JWT middleware — landing in A5.

	duration := time.Since(sess.CreatedAt)
	if err := s.registry.Delete(sess.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "registry.Delete: %v", err)
	}

	// Stop the audio pipeline FIRST so it can flush END_STREAM to the
	// engine while the PeerConnection is still up (DTLS/SRTP intact).
	// Closing the PC before the pipeline would not cause data loss,
	// but it would leave the last few in-flight PCM chunks without a
	// source and trigger spurious gRPC Send errors in logs.
	if raw, loaded := s.pipelineStops.LoadAndDelete(sess.ID); loaded {
		if fn, ok := raw.(func()); ok && fn != nil {
			fn()
		}
	}

	// Release the PeerConnection ICE sockets for this session.
	// Best-effort: a Close error does not fail the RPC because the
	// session is already removed from the registry and the meeting is
	// over from the caller's perspective.
	if s.webrtcNeg != nil {
		_ = s.webrtcNeg.Close(sess.ID)
	}

	return &aegisv1.EndMeetingResponse{
		Duration:          durationpb.New(duration),
		FinalSegmentCount: 0, // A4: read from engine final count.
		HintCount:         0, // A4: same.
	}, nil
}

// NegotiateWebRTC performs a non-trickle SDP exchange with the host.
//
// The caller must send a complete offer (ICE gathering state "complete")
// and wait for the returned answer before starting audio. Trickle ICE
// is not supported in this phase (ADR-0007 LAN assumption).
//
// When Config.WebRTCNegotiator is nil (not wired in the binary), this
// returns UNIMPLEMENTED so the frontend can detect the unconfigured
// state and fall back to the local WebSocket path.
func (s *GatewayService) NegotiateWebRTC(
	ctx context.Context,
	req *aegisv1.NegotiateWebRTCRequest,
) (*aegisv1.NegotiateWebRTCResponse, error) {
	if s.webrtcNeg == nil {
		return nil, status.Error(codes.Unimplemented, "WebRTC not configured on this gateway")
	}
	if req == nil || req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetOfferSdp() == "" {
		return nil, status.Error(codes.InvalidArgument, "offer_sdp is required")
	}

	sess, err := s.registry.Get(req.GetSessionId())
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return nil, status.Error(codes.NotFound, "session not found")
		}
		return nil, status.Errorf(codes.Internal, "registry.Get: %v", err)
	}
	if sess.Ended() {
		return nil, status.Error(codes.FailedPrecondition, "session already ended")
	}

	answerSDP, err := s.webrtcNeg.Negotiate(ctx, req.GetSessionId(), req.GetOfferSdp())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "WebRTC negotiate: %v", err)
	}

	// Start the audio pipeline now that the PeerConnection exists and
	// the Negotiator's AudioChan / ICEChan are populated. Errors here
	// are non-fatal for the RPC: returning the SDP lets the host
	// connect; absence of transcription is surfaced via viewer events
	// (or lack thereof) rather than a hard RPC failure. A stop handle
	// is stored under the session id so EndMeeting can tear it down.
	if s.pipelineStart != nil {
		stop, startErr := s.pipelineStart(sess, req.GetSessionId())
		if startErr != nil {
			// Log via the error return channel is out of scope here;
			// the pipeline package owns its own structured logging.
			// We surface a controlled Internal so upstream telemetry
			// notices, while still closing the negotiator to avoid
			// leaking an ICE agent without a consumer.
			_ = s.webrtcNeg.Close(req.GetSessionId())
			return nil, status.Errorf(codes.Internal, "audio pipeline start: %v", startErr)
		}
		if stop != nil {
			// Last-writer-wins on re-negotiation: if the host
			// re-negotiates, tear down the previous pipeline first so
			// there's at most one StreamTranscribe stream per session.
			if prev, loaded := s.pipelineStops.Swap(req.GetSessionId(), stop); loaded {
				if fn, ok := prev.(func()); ok && fn != nil {
					fn()
				}
			}
		}
	}

	return &aegisv1.NegotiateWebRTCResponse{AnswerSdp: answerSDP}, nil
}

// JoinAsViewer validates the token, registers the viewer in the
// session's viewer set, subscribes to the session fan-out, and
// streams every ViewerEvent the producer broadcasts until the peer
// disconnects or the session ends.
//
// Sequence numbers are per-subscription (proto comment on
// ViewerEvent.sequence): the kickoff ACTIVE event is seq=1, then
// each forwarded broadcast event is renumbered seq=2, 3, ... A
// dropped event on the fan-out channel surfaces as a gap in
// per-subscription sequence numbers.
//
// A4 wires the actual producer (engine StreamTranscribe consumer).
// For A5 the producer is the session.Broadcast call site — tests
// drive it directly; the WebSocket handler in internal/ws/ uses
// the same Subscribe primitive.
func (s *GatewayService) JoinAsViewer(
	req *aegisv1.JoinAsViewerRequest,
	stream aegisv1.Gateway_JoinAsViewerServer,
) error {
	if req == nil || req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetViewerToken() == "" {
		return status.Error(codes.Unauthenticated, "viewer_token is required")
	}

	if _, err := s.issuer.Verify(req.GetViewerToken(), req.GetSessionId()); err != nil {
		return status.Error(codes.PermissionDenied, "invalid viewer_token")
	}

	sess, err := s.registry.Get(req.GetSessionId())
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			return status.Error(codes.NotFound, "session not found")
		}
		return status.Errorf(codes.Internal, "registry.Get: %v", err)
	}
	if sess.Ended() {
		return status.Error(codes.FailedPrecondition, "session already ended")
	}

	// Viewer connection id is synthetic for A2. A3 ties this to the
	// underlying WebRTC / WebSocket connection id so grace-window
	// tracking can identify the host vs. viewers.
	connID, err := session.NewID()
	if err != nil {
		return status.Errorf(codes.Internal, "new conn id: %v", err)
	}
	sess.AddViewer(connID)
	defer sess.RemoveViewer(connID)

	// Subscribe BEFORE sending the kickoff so we don't miss events
	// produced concurrently with our join handshake.
	events, unsubscribe := sess.Subscribe(0) // 0 = ADR-0004 default capacity
	defer unsubscribe()

	var seq uint64 = 1
	kickoff := &aegisv1.ViewerEvent{
		Sequence:  seq,
		EmittedAt: timestamppb.New(time.Now()),
		Payload: &aegisv1.ViewerEvent_StateChange{
			StateChange: &aegisv1.MeetingStateChange{
				State:  aegisv1.MeetingState_MEETING_STATE_ACTIVE,
				Reason: "joined",
			},
		},
	}
	if err := stream.Send(kickoff); err != nil {
		return err
	}

	// Forward broadcast events until the channel closes (session
	// ended) or the client context cancels (client disconnected).
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Channel closed by MarkEnded — emit terminal ENDED.
				seq++
				final := &aegisv1.ViewerEvent{
					Sequence:  seq,
					EmittedAt: timestamppb.New(time.Now()),
					Payload: &aegisv1.ViewerEvent_StateChange{
						StateChange: &aegisv1.MeetingStateChange{
							State:  aegisv1.MeetingState_MEETING_STATE_ENDED,
							Reason: "session terminated",
						},
					},
				}
				_ = stream.Send(final) // best-effort
				return nil
			}
			seq++
			// Renumber per-subscription (proto comment on
			// ViewerEvent.sequence: "within a single subscription").
			// We build a fresh wrapper so we don't mutate the
			// shared *ViewerEvent that other subscribers also see;
			// the inner payload pointer is reused (read-only at
			// this point in the lifecycle).
			out := &aegisv1.ViewerEvent{
				Sequence:  seq,
				EmittedAt: ev.GetEmittedAt(),
				Payload:   ev.Payload,
			}
			if err := stream.Send(out); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}
