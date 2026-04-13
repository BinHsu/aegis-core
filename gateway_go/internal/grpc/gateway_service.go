// Package grpc wires the Go Gateway's gRPC server-side handlers.
//
// This file implements aegis.v1.Gateway — the client-facing service
// hosted by the Gateway. Phase 2 A2 scope:
//
//   - CreateMeeting — allocates a Session in the registry, issues a
//     viewer join JWT, reports engine metadata from the engine's
//     Health RPC.
//   - EndMeeting    — removes the session from the registry.
//   - JoinAsViewer  — token-validated server stream. For A2 it emits
//     a MEETING_STATE_ACTIVE state_change and then blocks on the
//     peer's context until the session ends or the caller disconnects.
//     Real transcript fan-out arrives in A4 (Pion audio pipeline).
//   - NegotiateWebRTC — returns UNIMPLEMENTED. A3 replaces this with a
//     full non-trickle SDP exchange.
//
// Naming note: we avoid importing this package as plain `grpc` because
// it collides with google.golang.org/grpc at call sites. Consumers
// typically alias it as `gatewaygrpc` or call New directly.
package grpc

import (
	"context"
	"errors"
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

// GatewayService implements aegisv1.GatewayServer.
type GatewayService struct {
	aegisv1.UnimplementedGatewayServer

	registry *session.Registry
	issuer   *token.Issuer
	health   healthProber

	engineProbeTimeout time.Duration
}

// Config bundles the dependencies of GatewayService so main.go wires
// them explicitly rather than reaching into package state.
type Config struct {
	Registry *session.Registry
	Issuer   *token.Issuer
	Engine   aegisv1.EngineClient

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
	return newWithProber(cfg.Registry, cfg.Issuer,
		func(ctx context.Context) (*aegisv1.HealthResponse, error) {
			return engine.Health(ctx, &aegisv1.HealthRequest{})
		},
		cfg.EngineProbeTimeout), nil
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

	return &aegisv1.EndMeetingResponse{
		Duration:          durationpb.New(duration),
		FinalSegmentCount: 0, // A4: read from engine final count.
		HintCount:         0, // A4: same.
	}, nil
}

// NegotiateWebRTC is the SDP-exchange entrypoint. A3 replaces this
// stub with a real Pion-driven answer; the explicit UNIMPLEMENTED
// here lets the frontend detect "cloud mode not ready yet" and fall
// back to the local WebSocket path (ADR-0007).
func (s *GatewayService) NegotiateWebRTC(
	ctx context.Context,
	req *aegisv1.NegotiateWebRTCRequest,
) (*aegisv1.NegotiateWebRTCResponse, error) {
	return nil, status.Error(codes.Unimplemented, "NegotiateWebRTC lands in Phase 2 A3")
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
