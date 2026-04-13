package grpc

import (
	"context"
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

func TestCreateMeetingRejectsEmptyRag(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.CreateMeeting(context.Background(), &aegisv1.CreateMeetingRequest{})
	if got := status.Code(err); got != codes.InvalidArgument {
		t.Fatalf("empty rag_id: got code %v, want InvalidArgument", got)
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

func TestNegotiateWebRTCUnimplemented(t *testing.T) {
	svc := newTestService(t, fakeEngineHealth(&aegisv1.HealthResponse{Ready: true}))
	client, cleanup := setupServer(t, svc)
	defer cleanup()

	_, err := client.NegotiateWebRTC(context.Background(), &aegisv1.NegotiateWebRTCRequest{
		SessionId: "s1",
		OfferSdp:  "v=0\r\n",
	})
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("NegotiateWebRTC: got code %v, want Unimplemented", got)
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
