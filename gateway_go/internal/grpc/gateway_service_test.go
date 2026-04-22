package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
)

// fakeEngineHealth returns a healthProber that replies with the given
// HealthResponse (and no error) on every call. Use this for the
// happy-path tests; the error-path tests supply their own closures.
func fakeEngineHealth(resp *aegisv1.HealthResponse) healthProber {
	return func(ctx context.Context) (*aegisv1.HealthResponse, error) {
		return resp, nil
	}
}

// setupServer wires a GatewayService behind a bufconn-backed gRPC
// server and returns a client talking to it. The returned cleanup
// closes everything so the test doesn't leak goroutines.
func setupServer(t *testing.T, svc *GatewayService) (aegisv1.GatewayClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	aegisv1.RegisterGatewayServer(srv, svc)
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
		lis.Close()
		t.Fatalf("grpc.NewClient: %v", err)
	}

	client := aegisv1.NewGatewayClient(conn)
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return client, cleanup
}

func newTestService(t *testing.T, prober healthProber) *GatewayService {
	t.Helper()
	reg := session.NewRegistry()
	issuer, err := token.NewIssuer()
	if err != nil {
		t.Fatalf("token.NewIssuer: %v", err)
	}
	return newWithProber(reg, issuer, prober, time.Second)
}

func TestCreateMeetingHappyPath(t *testing.T) {
	prober := fakeEngineHealth(&aegisv1.HealthResponse{
		Ready: true,
		Info: &aegisv1.EngineInfo{
			Model:   "whisper-large-v3-turbo-q4",
			Backend: "metal",
			Version: "0.1.0",
		},
	})
	svc := newTestService(t, prober)
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	resp, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
		Title: "Q2 pricing sync",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}
	if resp.GetSessionId() == "" {
		t.Fatalf("empty session_id")
	}
	if resp.GetViewerJoinToken() == "" {
		t.Fatalf("empty viewer_join_token")
	}
	if got := resp.GetEngine().GetModel(); got != "whisper-large-v3-turbo-q4" {
		t.Fatalf("EngineInfo.Model: got %q", got)
	}
	if !resp.GetTokenExpiresAt().AsTime().After(time.Now()) {
		t.Fatalf("TokenExpiresAt should be in the future; got %v", resp.GetTokenExpiresAt().AsTime())
	}

	// The session should now be in the registry.
	if got := svc.registry.Len(); got != 1 {
		t.Fatalf("registry.Len after CreateMeeting: got %d, want 1", got)
	}
}

// Per ADR-0023 §"Decision B — RAG opt-in", empty rag_id is a
// first-class mode (staff provides hints manually), not an error.
// Gateway MUST accept CreateMeeting with empty rag_id.
func TestCreateMeetingAcceptsEmptyRag(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	resp, err := client.CreateMeeting(
		context.Background(),
		&aegisv1.CreateMeetingRequest{}, // empty rag_id
	)
	if err != nil {
		t.Fatalf("CreateMeeting with empty rag_id: got error %v, want OK", err)
	}
	if resp.GetSessionId() == "" {
		t.Fatal("CreateMeeting with empty rag_id: empty session_id in response")
	}
	if svc.registry.Len() != 1 {
		t.Fatalf("registry.Len after empty-rag CreateMeeting: got %d, want 1",
			svc.registry.Len())
	}
}

func TestCreateMeetingRejectsLongTitle(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	title := make([]byte, 201)
	for i := range title {
		title[i] = 'x'
	}
	_, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
		Title: string(title),
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("oversize title: got code %v, want InvalidArgument", got)
	}
}

func TestCreateMeetingRejectsEngineNotReady(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: false}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("engine !ready: got code %v, want ResourceExhausted", got)
	}
}

func TestCreateMeetingReportsEngineUnavailable(t *testing.T) {
	prober := func(ctx context.Context) (*aegisv1.HealthResponse, error) {
		return nil, status.Error(codes.Unavailable, "engine down")
	}
	svc := newTestService(t, prober)
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("engine dead: got code %v, want Unavailable", got)
	}
}

func TestEndMeetingHappyPath(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	ended, err := client.EndMeeting(context.Background(), &aegisv1.EndMeetingRequest{
		SessionId: created.GetSessionId(),
	})
	if err != nil {
		t.Fatalf("EndMeeting: %v", err)
	}
	if ended.GetDuration() == nil {
		t.Fatalf("Duration should be non-nil")
	}
	if svc.registry.Len() != 0 {
		t.Fatalf("registry should be empty after EndMeeting, got %d", svc.registry.Len())
	}

	// Second EndMeeting is NotFound (registry.Delete already returned
	// ErrSessionNotFound; we translate to NOT_FOUND).
	_, err = client.EndMeeting(context.Background(), &aegisv1.EndMeetingRequest{
		SessionId: created.GetSessionId(),
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("second EndMeeting: got code %v, want NotFound", got)
	}
}

func TestEndMeetingUnknownSession(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.EndMeeting(context.Background(), &aegisv1.EndMeetingRequest{
		SessionId: "nope",
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("unknown: got code %v, want NotFound", got)
	}
}

// stubNegotiator implements WebRTCNegotiator for unit tests. It is
// defined inside package grpc (the internal test package) so it does
// not pull the Pion dependency into this test binary.
type stubNegotiator struct {
	negotiateFn  func(context.Context, string, string) (string, error)
	closedSessID string
}

func (s *stubNegotiator) Negotiate(ctx context.Context, sessionID, offerSDP string) (string, error) {
	if s.negotiateFn != nil {
		return s.negotiateFn(ctx, sessionID, offerSDP)
	}
	return "v=0\r\na=stub-answer\r\n", nil
}

func (s *stubNegotiator) Close(sessionID string) error {
	s.closedSessID = sessionID
	return nil
}

// TestNegotiateWebRTCUnimplemented verifies that a GatewayService wired
// without a WebRTCNegotiator (the nil/test path) returns UNIMPLEMENTED.
func TestNegotiateWebRTCUnimplemented(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.NegotiateWebRTC(context.Background(), &aegisv1.NegotiateWebRTCRequest{
		SessionId: "s1",
		OfferSdp:  "v=0\r\n",
	})
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("NegotiateWebRTC (nil neg): got code %v, want Unimplemented", got)
	}
}

// TestNegotiateWebRTCDelegatesToNegotiator verifies that, when a
// WebRTCNegotiator is injected, NegotiateWebRTC calls it and returns
// its answer SDP.
func TestNegotiateWebRTCDelegatesToNegotiator(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	stub := &stubNegotiator{}
	svc.webrtcNeg = stub
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-1",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	resp, err := client.NegotiateWebRTC(context.Background(), &aegisv1.NegotiateWebRTCRequest{
		SessionId: created.GetSessionId(),
		OfferSdp:  "v=0\r\n",
	})
	if err != nil {
		t.Fatalf("NegotiateWebRTC: %v", err)
	}
	if resp.GetAnswerSdp() != "v=0\r\na=stub-answer\r\n" {
		t.Fatalf("answer_sdp: got %q", resp.GetAnswerSdp())
	}
}

// TestNegotiateWebRTCValidatesInputs covers INVALID_ARGUMENT and
// NOT_FOUND paths on NegotiateWebRTC.
func TestNegotiateWebRTCValidatesInputs(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	svc.webrtcNeg = &stubNegotiator{}
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	tests := []struct {
		name string
		req  *aegisv1.NegotiateWebRTCRequest
		want codes.Code
	}{
		{
			name: "missing session_id",
			req:  &aegisv1.NegotiateWebRTCRequest{OfferSdp: "v=0\r\n"},
			want: codes.InvalidArgument,
		},
		{
			name: "missing offer_sdp",
			req:  &aegisv1.NegotiateWebRTCRequest{SessionId: "s1"},
			want: codes.InvalidArgument,
		},
		{
			name: "unknown session",
			req:  &aegisv1.NegotiateWebRTCRequest{SessionId: "ghost", OfferSdp: "v=0\r\n"},
			want: codes.NotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.NegotiateWebRTC(context.Background(), tc.req)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("%s: got code %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestEndMeetingCallsNegotiatorClose verifies that EndMeeting invokes
// WebRTCNegotiator.Close with the correct session ID.
func TestEndMeetingCallsNegotiatorClose(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	stub := &stubNegotiator{}
	svc.webrtcNeg = stub
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-1",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	if _, err := client.EndMeeting(context.Background(), &aegisv1.EndMeetingRequest{
		SessionId: created.GetSessionId(),
	}); err != nil {
		t.Fatalf("EndMeeting: %v", err)
	}

	if stub.closedSessID != created.GetSessionId() {
		t.Fatalf("Negotiator.Close not called with session id; got %q, want %q",
			stub.closedSessID, created.GetSessionId())
	}
}

func TestJoinAsViewerRejectsBadToken(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	// Present a syntactically-plausible but unsigned token.
	stream, err := client.JoinAsViewer(context.Background(), &aegisv1.JoinAsViewerRequest{
		SessionId:   created.GetSessionId(),
		ViewerToken: "x.y.z",
	})
	if err != nil {
		t.Fatalf("JoinAsViewer dial: %v", err)
	}
	_, err = stream.Recv()
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("bad token: got code %v, want PermissionDenied", got)
	}
}

func TestJoinAsViewerRejectsMissingToken(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	stream, err := client.JoinAsViewer(context.Background(), &aegisv1.JoinAsViewerRequest{
		SessionId: "s1",
	})
	if err != nil {
		t.Fatalf("JoinAsViewer dial: %v", err)
	}
	_, err = stream.Recv()
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("missing token: got code %v, want Unauthenticated", got)
	}
}

func TestJoinAsViewerDeliversBroadcastThenEnd(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.JoinAsViewer(ctx, &aegisv1.JoinAsViewerRequest{
		SessionId:   created.GetSessionId(),
		ViewerToken: created.GetViewerJoinToken(),
	})
	if err != nil {
		t.Fatalf("JoinAsViewer dial: %v", err)
	}

	// Kickoff event: MEETING_STATE_ACTIVE seq=1.
	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv kickoff: %v", err)
	}
	if got := ev.GetSequence(); got != 1 {
		t.Fatalf("kickoff sequence: got %d, want 1", got)
	}
	if got := ev.GetStateChange().GetState(); got != aegisv1.MeetingState_MEETING_STATE_ACTIVE {
		t.Fatalf("kickoff state: got %v, want ACTIVE", got)
	}

	// Wait for the server-side Subscribe to have registered. We can
	// either poll SubscriberCount or just give it a moment.
	sess, err := svc.registry.Get(created.GetSessionId())
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for sess.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sess.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount after Subscribe: got %d, want 1", got)
	}

	// Broadcast a transcript event — the gRPC handler must forward it.
	delivered, dropped := sess.Broadcast(&aegisv1.ViewerEvent{
		Payload: &aegisv1.ViewerEvent_Transcript{
			Transcript: &aegisv1.TranscriptSegment{
				SegmentId:    1,
				SpeakerLabel: "Speaker_0",
				Text:         "hello world",
				IsFinal:      true,
			},
		},
	})
	if delivered != 1 || dropped != 0 {
		t.Fatalf("Broadcast: %d/%d, want 1/0", delivered, dropped)
	}

	ev, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv broadcast: %v", err)
	}
	if got := ev.GetSequence(); got != 2 {
		t.Fatalf("broadcast sequence: got %d, want 2 (per-subscription numbering)", got)
	}
	tx := ev.GetTranscript()
	if tx == nil {
		t.Fatalf("expected transcript payload, got %+v", ev)
	}
	if tx.GetText() != "hello world" {
		t.Fatalf("transcript text: got %q", tx.GetText())
	}

	// End the meeting → handler must emit terminal ENDED then close.
	if _, err := client.EndMeeting(ctx, &aegisv1.EndMeetingRequest{
		SessionId: created.GetSessionId(),
	}); err != nil {
		t.Fatalf("EndMeeting: %v", err)
	}

	ev, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv ENDED: %v", err)
	}
	if got := ev.GetStateChange().GetState(); got != aegisv1.MeetingState_MEETING_STATE_ENDED {
		t.Fatalf("terminal state: got %v, want ENDED", got)
	}

	// After ENDED, the stream closes (io.EOF or other close error).
	if _, err := stream.Recv(); err == nil {
		t.Fatalf("expected stream close after ENDED, got more events")
	}
}

func TestJoinAsViewerDeliversKickoffThenEnd(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-42",
	})
	if err != nil {
		t.Fatalf("CreateMeeting: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := client.JoinAsViewer(ctx, &aegisv1.JoinAsViewerRequest{
		SessionId:   created.GetSessionId(),
		ViewerToken: created.GetViewerJoinToken(),
	})
	if err != nil {
		t.Fatalf("JoinAsViewer dial: %v", err)
	}

	// Kickoff event: MEETING_STATE_ACTIVE.
	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv kickoff: %v", err)
	}
	sc := ev.GetStateChange()
	if sc == nil || sc.GetState() != aegisv1.MeetingState_MEETING_STATE_ACTIVE {
		t.Fatalf("expected ACTIVE state_change, got %+v", ev)
	}
	if ev.GetSequence() != 1 {
		t.Fatalf("kickoff sequence: got %d, want 1", ev.GetSequence())
	}

	// The viewer should now be counted on the session.
	sess, err := svc.registry.Get(created.GetSessionId())
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	// Eventually — the AddViewer call happens on the server goroutine.
	deadline := time.Now().Add(time.Second)
	for sess.ViewerCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sess.ViewerCount(); got != 1 {
		t.Fatalf("viewer count: got %d, want 1", got)
	}

	// End the meeting via the public RPC. The stream must then emit
	// a terminal ENDED event (best-effort; might be dropped if the
	// context cancels first, so we allow either ENDED or EOF).
	if _, err := client.EndMeeting(ctx, &aegisv1.EndMeetingRequest{
		SessionId: created.GetSessionId(),
	}); err != nil {
		t.Fatalf("EndMeeting: %v", err)
	}

	// Give the server a moment to drop us. Since A2's JoinAsViewer
	// only unblocks on ctx.Done, we cancel the stream context to
	// trigger the teardown path.
	cancel()
	for {
		_, err := stream.Recv()
		if err != nil {
			return // stream closed — expected.
		}
	}
}

// -----------------------------------------------------------------------------
// SendOfficerHint — staff-authored hint broadcast (Phase 3c hint fan-out slice)
// -----------------------------------------------------------------------------

// officerHintClient bundles a fresh service + session + valid token so
// each test doesn't re-implement the three-step setup.
func officerHintClient(t *testing.T) (
	svc *GatewayService,
	client aegisv1.GatewayClient,
	sessionID, token string,
	cleanup func(),
) {
	t.Helper()
	svc = newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup = setupServer(t, svc)
	created, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{
		RagId: "corpus-officer",
	})
	if err != nil {
		cleanup()
		t.Fatalf("CreateMeeting: %v", err)
	}
	return svc, client, created.GetSessionId(), created.GetViewerJoinToken(), cleanup
}

func TestSendOfficerHintRejectsMissingSessionID(t *testing.T) {
	_, client, _, token, cleanup := officerHintClient(t)
	defer cleanup()
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		ViewerToken: token,
		Suggestion:  "override",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("missing session_id: got code %v, want InvalidArgument", got)
	}
}

func TestSendOfficerHintRejectsMissingToken(t *testing.T) {
	_, client, sessionID, _, cleanup := officerHintClient(t)
	defer cleanup()
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:  sessionID,
		Suggestion: "override",
		Urgency:    aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("missing token: got code %v, want Unauthenticated", got)
	}
}

func TestSendOfficerHintRejectsBadToken(t *testing.T) {
	_, client, sessionID, _, cleanup := officerHintClient(t)
	defer cleanup()
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: "not-a-real-jwt",
		Suggestion:  "override",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("bad token: got code %v, want PermissionDenied", got)
	}
}

func TestSendOfficerHintRejectsEmptySuggestion(t *testing.T) {
	_, client, sessionID, token, cleanup := officerHintClient(t)
	defer cleanup()
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  "",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("empty suggestion: got code %v, want InvalidArgument", got)
	}
}

func TestSendOfficerHintRejectsOversizedSuggestion(t *testing.T) {
	_, client, sessionID, token, cleanup := officerHintClient(t)
	defer cleanup()
	big := make([]byte, maxOfficerHintSuggestionBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  string(big),
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("oversized suggestion: got code %v, want InvalidArgument", got)
	}
}

func TestSendOfficerHintRejectsUnspecifiedUrgency(t *testing.T) {
	_, client, sessionID, token, cleanup := officerHintClient(t)
	defer cleanup()
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  "urgent note",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_UNSPECIFIED,
	})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("unspecified urgency: got code %v, want InvalidArgument", got)
	}
}

func TestSendOfficerHintRejectsUnknownSession(t *testing.T) {
	_, client, _, token, cleanup := officerHintClient(t)
	defer cleanup()
	// Token was issued for a real session, but we ask about a different
	// (unknown) session. Token.Verify checks the session binding, so
	// the expected error is PermissionDenied — NOT NotFound. That is
	// the intended posture: we never leak "session with id X exists
	// but you lack access" vs "session X does not exist."
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   "nonexistent-session",
		ViewerToken: token,
		Suggestion:  "override",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_NORMAL,
	})
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("unknown session (token mismatch): got code %v, want PermissionDenied",
			got)
	}
}

func TestSendOfficerHintRejectsEndedSession(t *testing.T) {
	_, client, sessionID, token, cleanup := officerHintClient(t)
	defer cleanup()
	// End the meeting first.
	if _, err := client.EndMeeting(context.Background(),
		&aegisv1.EndMeetingRequest{SessionId: sessionID}); err != nil {
		t.Fatalf("EndMeeting: %v", err)
	}
	// Now the session is gone from the registry → NotFound (Registry.Delete
	// calls MarkEnded + removes). This is the current CreateMeeting /
	// EndMeeting contract; if EndMeeting moves to "mark ended but keep
	// in registry", this assertion flips to FailedPrecondition — both
	// are defensible, but the code path below the switch is identical,
	// so pin to whichever the current implementation chooses.
	_, err := client.SendOfficerHint(context.Background(), &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  "too late",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_HIGH,
	})
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("ended session: got code %v, want NotFound (EndMeeting removes from registry)",
			got)
	}
}

func TestSendOfficerHintHappyPathBroadcastsToSubscriber(t *testing.T) {
	svc, client, sessionID, token, cleanup := officerHintClient(t)
	defer cleanup()

	// Subscribe a viewer stream FIRST so we catch the broadcast from
	// the very next moment on.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.JoinAsViewer(ctx, &aegisv1.JoinAsViewerRequest{
		SessionId:   sessionID,
		ViewerToken: token,
	})
	if err != nil {
		t.Fatalf("JoinAsViewer dial: %v", err)
	}
	// Drain kickoff (seq=1, ACTIVE state_change).
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("Recv kickoff: %v", err)
	}

	// Wait for the server-side Subscribe to register so SendOfficerHint
	// doesn't race past a not-yet-installed subscriber.
	sess, err := svc.registry.Get(sessionID)
	if err != nil {
		t.Fatalf("registry.Get: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for sess.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sess.SubscriberCount(); got != 1 {
		t.Fatalf("SubscriberCount after Subscribe: got %d, want 1", got)
	}

	// First officer hint — urgency HIGH, with rationale.
	resp1, err := client.SendOfficerHint(ctx, &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  "Skip the Q4 number — it's stale.",
		Rationale:   "Retriever cited a pre-forecast deck.",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_HIGH,
	})
	if err != nil {
		t.Fatalf("SendOfficerHint #1: %v", err)
	}
	if resp1.GetHintId() != 1 {
		t.Fatalf("hint_id #1: got %d, want 1 (monotonic start)", resp1.GetHintId())
	}

	ev, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv broadcast #1: %v", err)
	}
	if got := ev.GetSequence(); got != 2 {
		t.Fatalf("broadcast #1 sequence: got %d, want 2 (per-subscription renumbering)", got)
	}
	h := ev.GetHint()
	if h == nil {
		t.Fatalf("expected hint payload, got %+v", ev)
	}
	if h.GetHintId() != 1 {
		t.Fatalf("broadcast hint_id: got %d, want 1", h.GetHintId())
	}
	if h.GetSuggestion() != "Skip the Q4 number — it's stale." {
		t.Fatalf("suggestion: got %q", h.GetSuggestion())
	}
	if h.GetRationale() != "Retriever cited a pre-forecast deck." {
		t.Fatalf("rationale: got %q", h.GetRationale())
	}
	if h.GetUrgency() != aegisv1.HintUrgency_HINT_URGENCY_HIGH {
		t.Fatalf("urgency: got %v, want HIGH", h.GetUrgency())
	}
	if len(h.GetCitations()) != 0 {
		t.Fatalf("citations: got %d, want 0 (staff-authored has no RAG source)",
			len(h.GetCitations()))
	}

	// Second officer hint — monotonic id must increment.
	resp2, err := client.SendOfficerHint(ctx, &aegisv1.SendOfficerHintRequest{
		SessionId:   sessionID,
		ViewerToken: token,
		Suggestion:  "Don't commit to Q3 targets publicly.",
		Urgency:     aegisv1.HintUrgency_HINT_URGENCY_URGENT,
	})
	if err != nil {
		t.Fatalf("SendOfficerHint #2: %v", err)
	}
	if resp2.GetHintId() != 2 {
		t.Fatalf("hint_id #2: got %d, want 2", resp2.GetHintId())
	}

	ev, err = stream.Recv()
	if err != nil {
		t.Fatalf("Recv broadcast #2: %v", err)
	}
	if got := ev.GetHint().GetHintId(); got != 2 {
		t.Fatalf("broadcast #2 hint_id: got %d, want 2", got)
	}
}

// -----------------------------------------------------------------------------
// ListCorpora
// -----------------------------------------------------------------------------

func TestListCorporaUnimplementedWhenNotWired(t *testing.T) {
	// newTestService uses newWithProber which does NOT set listCorpora.
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.ListCorpora(context.Background(), &aegisv1.ListCorporaRequest{})
	if err == nil {
		t.Fatal("expected UNIMPLEMENTED error, got nil")
	}
	if got, want := status.Code(err), codes.Unimplemented; got != want {
		t.Fatalf("status code: got %v, want %v", got, want)
	}
}

func TestListCorporaOverridesTenantIDToDemo(t *testing.T) {
	// The gateway must substitute phase3DefaultTenant regardless of what
	// the client sent — guards against client enumeration of other
	// tenants' collection names (ADR-0022 enforcement point).
	var capturedTenant string
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	svc.listCorpora = func(ctx context.Context, tenantID string) (*aegisv1.ListCorporaResponse, error) {
		capturedTenant = tenantID
		return &aegisv1.ListCorporaResponse{
			Corpora: []*aegisv1.CorpusInfo{
				{Id: "aegis_demo_taiwan", Label: "taiwan"},
			},
		}, nil
	}
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	// Client tries to impersonate a different tenant.
	resp, err := client.ListCorpora(context.Background(), &aegisv1.ListCorporaRequest{
		TenantId: "attacker-tenant",
	})
	if err != nil {
		t.Fatalf("ListCorpora: %v", err)
	}
	if capturedTenant != "demo" {
		t.Fatalf("gateway did not override tenant_id: got %q, want %q",
			capturedTenant, "demo")
	}
	if len(resp.GetCorpora()) != 1 {
		t.Fatalf("got %d corpora, want 1", len(resp.GetCorpora()))
	}
	if got := resp.GetCorpora()[0].GetId(); got != "aegis_demo_taiwan" {
		t.Fatalf("corpus id: got %q, want %q", got, "aegis_demo_taiwan")
	}
}

func TestListCorporaUnavailableOnEngineError(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	svc.listCorpora = func(ctx context.Context, tenantID string) (*aegisv1.ListCorporaResponse, error) {
		return nil, errors.New("engine offline")
	}
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.ListCorpora(context.Background(), &aegisv1.ListCorporaRequest{})
	if err == nil {
		t.Fatal("expected UNAVAILABLE error, got nil")
	}
	if got, want := status.Code(err), codes.Unavailable; got != want {
		t.Fatalf("status code: got %v, want %v", got, want)
	}
}
